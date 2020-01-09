package main

import (
	elastic "gopkg.in/olivere/elastic.v3"

	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"time"

	"github.com/dgrijalva/jwt-go"
)

const (
	TYPE_USER = "user"
)

var (
	usernamePattern = regexp.MustCompile(`^[a-z0-9_]+$`).MatchString
)

type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Age      int    `json:"age"`
	Gender   string `json:"gender"`
}

// checkUser checks whether user is valid
func checkUser(username, password string) bool {
	// 连接es，在es中查询user
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		fmt.Printf("ES is not setup %v\n", err)
		panic(err)
		return false
	}

	// Search with a term query
	termQuery := elastic.NewTermQuery("username", username)
	// search the username in the elasticSearch sever's database
	queryResult, err := es_client.Search().
		Index(INDEX).
		Query(termQuery).
		Pretty(true).
		Do()
	if err != nil {
		fmt.Printf("ES query failed %v\n", err)
		return false
	}

	var tyu User
	for _, item := range queryResult.Each(reflect.TypeOf(tyu)) {
		u := item.(User)
		return u.Password == password && u.Username == username
	}
	// If no user exist, return false.
	return false
}

// Add a new user. Return true if successfully.
func addUser(user User) bool {
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		fmt.Printf("ES is not setup %v\n", err)
		return false
	}

	// search the username, if it has already existedm then stop
	termQuery := elastic.NewTermQuery("username", user.Username)
	queryResult, err := es_client.Search().
		Index(INDEX).
		Query(termQuery).
		Pretty(true).
		Do()
	if err != nil {
		fmt.Printf("ES query failed %v\n", err)
		return false
	}

	// if the query result has more than 0 records, which means the username has already be taken
	if queryResult.TotalHits() > 0 {
		fmt.Printf("User %s already exists, cannot create duplicate user.\n", user.Username)
		return false
	}

	// use the index method, to insert a new record into the elastic search database
	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE_USER).
		Id(user.Username).
		BodyJson(user).
		Refresh(true).
		Do()
	if err != nil {
		fmt.Printf("ES save user failed %v\n", err)
		return false
	}

	return true
}

// If signup is successful, a new session is created (the token when we use the jwt).
func signupHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one signup request")

	// decode and read json
	decoder := json.NewDecoder(r.Body)
	var u User
	if err := decoder.Decode(&u); err != nil {
		panic(err)
		return
	}

	// the condition is used to check whether the password or username is valid
	if u.Username != "" && u.Password != "" && usernamePattern(u.Username) {
		if addUser(u) {
			fmt.Println("User added successfully.")
			w.Write([]byte("User added successfully."))
		} else {
			fmt.Println("Failed to add a new user.")
			http.Error(w, "Failed to add a new user", http.StatusInternalServerError)
		}
	} else {
		fmt.Println("Empty password or username.")
		http.Error(w, "Empty password or username", http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// If login is successful, a new token is created.
func loginHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one login request")

	// decode and read the json into the u variable
	decoder := json.NewDecoder(r.Body)
	var u User
	if err := decoder.Decode(&u); err != nil {
		panic(err)
		return
	}

	if checkUser(u.Username, u.Password) {
		// create a token with sha256
		token := jwt.New(jwt.SigningMethodHS256)
		// 就是 jwt.io 例子中的 play load 部分
		claims := token.Claims.(jwt.MapClaims)
		/* Set token claims */
		claims["username"] = u.Username
		// 过期的时间
		claims["exp"] = time.Now().Add(time.Hour * 24).Unix()

		/* Sign the token with our secret */
		tokenString, _ := token.SignedString(mySigningKey)

		/* Finally, write the token to the browser window */
		w.Write([]byte(tokenString))
	} else {
		fmt.Println("Invalid password or username.")
		http.Error(w, "Invalid password or username", http.StatusForbidden)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
