package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"errors"
	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
	"github.com/nirasan/gae-jwt/bindata"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"strings"
)

// App Engine のメイン実行ファイルの init 関数から利用されるルーティング設定を返却する関数
func NewHandler() http.Handler {
	// ルータの初期化
	r := mux.NewRouter()
	// ユーザー登録
	r.HandleFunc("/registration", RegistrationHandler)
	// ユーザー認証
	r.HandleFunc("/authentication", AuthenticationHandler)
	// 認証済みユーザーのみ閲覧可能なコンテンツ
	r.HandleFunc("/authorized_hello", AuthorizedHelloWorldHandler)
	// だれでも閲覧可能なコンテンツ
	r.HandleFunc("/hello", HelloWorldHandler)
	// ルータの返却
	return r
}

// ユーザー認証データ
type UserAuthentication struct {
	Username string
	Password string
}

// registration のリクエスト型
type RegistrationHandlerRequest struct {
	Username string
	Password string
}

// registration のレスポンス型
type RegistrationHandlerResponse struct {
	Success bool
}

// authentication のリクエスト型
type AuthenticationHandlerRequest struct {
	Username string
	Password string
}

// authentication のレスポンス型
type AuthenticationHandlerResponse struct {
	Success bool
	Token   string
}

// コンテンツ共通のレスポンス型
type HelloWorldHandlerResponse struct {
	Success bool
	Message string
}

// ユーザー登録処理
// ユーザー名とパスワードを受け取って Datastore に登録する
func RegistrationHandler(w http.ResponseWriter, r *http.Request) {

	// POST のペイロードで JSON を受け取ってリクエスト型にデコードする
	var req RegistrationHandlerRequest
	DecodeJson(r, &req)

	// ユーザー情報の登録準備
	ctx := appengine.NewContext(r)
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		panic(err.Error())
	}
	ua := UserAuthentication{Username: req.Username, Password: string(hashedPassword)}

	// Datastore へユーザー情報を登録
	err = datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		key := datastore.NewKey(ctx, "UserAuthentication", req.Username, 0, nil)
		var userAuthentication UserAuthentication
		if err := datastore.Get(ctx, key, &userAuthentication); err != datastore.ErrNoSuchEntity {
			return errors.New("user already exist")
		}
		if _, err := datastore.Put(ctx, key, &ua); err == nil {
			return nil
		} else {
			return err
		}
	}, nil)

	if err == nil {
		EncodeJson(w, RegistrationHandlerResponse{Success: true})
	} else {
		EncodeJson(w, RegistrationHandlerResponse{Success: false})
	}
}

// ユーザー認証処理
// ユーザー名とパスワードを受け取ってユーザーが存在したら JWT のトークンを返す
func AuthenticationHandler(w http.ResponseWriter, r *http.Request) {

	// リクエスト型のデコード
	var req AuthenticationHandlerRequest
	DecodeJson(r, &req)

	// ユーザーが存在するかどうか確認
	ctx := appengine.NewContext(r)
	key := datastore.NewKey(ctx, "UserAuthentication", req.Username, 0, nil)
	var userAuthentication UserAuthentication
	if err := datastore.Get(ctx, key, &userAuthentication); err != nil {
		EncodeJson(w, AuthenticationHandlerResponse{Success: false})
		return
	}
	// パスワードの検証
	if err := bcrypt.CompareHashAndPassword([]byte(userAuthentication.Password), []byte(req.Password)); err != nil {
		EncodeJson(w, AuthenticationHandlerResponse{Success: false})
		return
	}

	// 秘密鍵を go-bindata で固めたデータから取得
	pem, e := bindata.Asset("assets/ec256-key-pri.pem")
	if e != nil {
		panic(e.Error())
	}
	// 署名アルゴリズムの作成
	method := jwt.GetSigningMethod("ES256")
	// トークンの作成
	token := jwt.NewWithClaims(method, jwt.MapClaims{
		"sub": req.Username,
		"exp": time.Now().Add(time.Hour * 1).Unix(),
	})
	// 秘密鍵のパース
	privateKey, e := jwt.ParseECPrivateKeyFromPEM(pem)
	if e != nil {
		panic(e.Error())
	}
	// トークンの署名
	signedToken, e := token.SignedString(privateKey)
	if e != nil {
		panic(e.Error())
	}

	// JSON でトークンを返却
	EncodeJson(w, AuthenticationHandlerResponse{Success: true, Token: signedToken})
}

// 誰でも閲覧可能なコンテンツ
func HelloWorldHandler(w http.ResponseWriter, r *http.Request) {
	EncodeJson(w, HelloWorldHandlerResponse{Success: true, Message: "Hello World"})
}

// 認証済みのユーザーのみ閲覧可能なコンテンツ
func AuthorizedHelloWorldHandler(w http.ResponseWriter, r *http.Request) {

	// Authorization ヘッダーに入っているトークンを検証する
	token, e := Authorization(r)

	if e != nil {
		EncodeJson(w, HelloWorldHandlerResponse{Success: false})
	}

	// トークンからユーザー名を取得してレスポンスに記載する
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		EncodeJson(w, HelloWorldHandlerResponse{Success: true, Message: "Hello " + claims["sub"].(string)})
	}
}

// トークンの認可
func Authorization(r *http.Request) (*jwt.Token, error) {

	// Authorization ヘッダーの取得
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, errors.New("Invalid authorization hader")
	}

	// Authorization ヘッダーの解析
	// "Authorization: Bearer <TOKEN>" の形式を想定している
	parts := strings.SplitN(header, " ", 2)
	if !(len(parts) == 2 && parts[0] == "Bearer") {
		return nil, errors.New("Invalid authorization hader")
	}

	// トークンの展開
	// ハッシュ化されているトークンを *jwt.Token 型に変換する
	token, e := jwt.Parse(parts[1], func(t *jwt.Token) (interface{}, error) {

		// 署名アルゴリズムの検証
		method := jwt.GetSigningMethod("ES256")
		if method != t.Method {
			return nil, errors.New("Invalid signing method")
		}

		// go-bindata で固められた公開鍵を読み込む
		pem, e := bindata.Asset("assets/ec256-key-pub.pem")
		if e != nil {
			return nil, e
		}

		// 公開鍵のパース
		key, e := jwt.ParseECPublicKeyFromPEM(pem)
		if e != nil {
			return nil, e
		}

		// 公開鍵を復号化に使うデータとして返却
		return key, nil
	})
	if e != nil {
		return nil, errors.New(e.Error())
	}

	// トークンの検証
	if _, ok := token.Claims.(jwt.MapClaims); !ok || !token.Valid {
		return nil, errors.New("Invalid token")
	}

	return token, nil
}

// POST された JSON データをデコードする
func DecodeJson(r *http.Request, data interface{}) {
	decoder := json.NewDecoder(r.Body)
	defer r.Body.Close()
	if e := decoder.Decode(data); e != nil {
		panic(e.Error())
	}
}

// JSON データでレスポンスを行う
func EncodeJson(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
