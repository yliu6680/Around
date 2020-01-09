package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strconv"

	"github.com/google/uuid"
	elastic "gopkg.in/olivere/elastic.v3"

	"context"

	"cloud.google.com/go/storage"
	jwtmiddleware "github.com/auth0/go-jwt-middleware"

	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
)

type Location struct {
	Lat float64 `json:"lat"` // `` 反转义
	Lon float64 `json:"lon"`
}

// POST object for storing post request
type Post struct {
	// `json:"user"` is for the json parsing of this User field. Otherwise, by default it's 'User'.
	User     string   `json:"user"`
	Message  string   `json:"message"`
	Location Location `json:"location"`
	Url      string   `json:"url"`
}

// constant used in the environment, ES_URL for the elastic search GCE's ip
const (
	INDEX       = "around"
	TYPE        = "post"
	DISTANCE    = "200km"
	ES_URL      = "http://104.197.125.211:9200/"
	BUCKET_NAME = "post-images-263423"
)

var mySigningKey = []byte("yuanrongdesecret")

func main() {
	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Use the IndexExists service to check if a specified index exists.
	exists, err := client.IndexExists(INDEX).Do()

	if err != nil {
		panic(err)
	}

	if !exists {
		// Create a new index.
		// change the type to geo_point
		mapping := `{
			"mappings":{
				"post":{
					"properties":{
						"location":{
							"type":"geo_point"
						}
					}
				}
			}
		}`
		_, err := client.CreateIndex(INDEX).Body(mapping).Do()
		if err != nil {
			// Handle error
			panic(err)
		}
	}

	fmt.Println("started-service")

	r := mux.NewRouter()

	// new jwtMiddleware，用于handle 用户的 token
	var jwtMiddleware = jwtmiddleware.New(jwtmiddleware.Options{
		// get the secret key, error 是一个类型
		ValidationKeyGetter: func(token *jwt.Token) (interface{}, error) {
			return mySigningKey, nil
		},
		SigningMethod: jwt.SigningMethodHS256,
	})

	// 两个handler是一直存在的，并且并发的，当http在8080监听到request时，再调用不同的handler
	// http.HandleFunc("/post", handlerPost)
	// 之前直接递交给http的handler，现在用mux的router包装一层，传递到jwtmiddleware，middleware可以判断用户的token是否valid
	r.Handle("/post", jwtMiddleware.Handler(http.HandlerFunc(handlerPost))).Methods("POST")
	r.Handle("/search", jwtMiddleware.Handler(http.HandlerFunc(handlerSearch))).Methods("GET")
	r.Handle("/login", http.HandlerFunc(loginHandler)).Methods("POST")
	r.Handle("/signup", http.HandlerFunc(signupHandler)).Methods("POST")

	http.Handle("/", r)
	log.Fatal(http.ListenAndServe(":8080", nil))

	// http.HandleFunc("/search", handlerSearch)
	// listenAndServe run the server on 8080 port, the nil is the handler, we have defined before
	// log.Fatal(http.ListenAndServe(":8080", nil))
}

func handlerPost(w http.ResponseWriter, r *http.Request) { // * is pointer, means it is not a copy of the
	// object; Similar to python, in go, all input are like primitive pytes in Java, it is a copy of the original object.
	// Parse from body of request to get a json object.

	//USE FOR JSON DATA
	// fmt.Println("Received one post request")
	// decoder := json.NewDecoder(r.Body)
	// var p Post
	// if err := decoder.Decode(&p); err != nil { // &传地址
	// 	panic(err)
	// 	return
	// }

	// fmt.Fprintf(w, "Post received: %s\n", p.Message) // save the message to the w object

	// // generate an unique id for each post
	// id := uuid.New().String()
	// fmt.Println(reflect.TypeOf(id))
	// fmt.Println(reflect.TypeOf(123))
	// // Save to ES.
	// saveToES(&p, id)

	// w is the response, so all these set are setting for the response to users
	// * means all address could use the the response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	user := r.Context().Value("user")
	// transfer to claims, to get the data we need (save in the playload in jwt tutorial)
	claims := user.(*jwt.Token).Claims
	// get the user name
	username := claims.(jwt.MapClaims)["username"]

	// PARSE FORM DATA
	// 32 << 20 is the maxMemory param for ParseMultipartForm, equals to 32MB (1MB = 1024 * 1024 bytes = 2^20 bytes)
	// After you call ParseMultipartForm, the file will be saved in the server memory with maxMemory size.
	// If the file size is larger than maxMemory, the rest of the data will be saved in a system temporary file.
	r.ParseMultipartForm(32 << 20)

	// Parse from form data.
	// Parse string text/
	fmt.Printf("Received one post request %s\n", r.FormValue("message"))
	lat, _ := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, _ := strconv.ParseFloat(r.FormValue("lon"), 64)
	p := &Post{
		User:    username.(string),
		Message: r.FormValue("message"),
		Location: Location{
			Lat: lat,
			Lon: lon,
		},
	}

	// generate an unique id for each post
	id := uuid.New().String()

	// parse image in the form
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "Image is not available", http.StatusInternalServerError)
		fmt.Printf("Image is not available %v.\n", err)
		return
	}
	defer file.Close()

	// GCS's credential (password to connect GCS)
	ctx := context.Background()

	// replace it with your real bucket name.
	_, attrs, err := saveToGCS(ctx, file, BUCKET_NAME, id)
	if err != nil {
		http.Error(w, "GCS is not setup", http.StatusInternalServerError)
		fmt.Printf("GCS is not setup %v\n", err)
		return
	}

	// Update the media link into the post request
	p.Url = attrs.MediaLink

	// Save the post request to ES engine.
	saveToES(p, id)

	// Save to BigTable.
	//saveToBigTable(p, id)

}

// Save a post to GCS
func saveToGCS(ctx context.Context, r io.Reader, bucketName, name string) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	// connect to the GCS client
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer client.Close()

	// Sets the name for the new bucket, we have created on console, so we just use the name we used in console
	// bucketName := "post-images-263423"

	// use the bucket we have created
	bucket := client.Bucket(bucketName)

	// Next check if the bucket exists
	if _, err = bucket.Attrs(ctx); err != nil {
		return nil, nil, err
	}

	// create the object in the bucket
	obj := bucket.Object(name)
	// create the object's writer
	w := obj.NewWriter(ctx)
	// io.copy write the content into the obj
	if _, err := io.Copy(w, r); err != nil {
		return nil, nil, err
	}
	if err := w.Close(); err != nil {
		return nil, nil, err
	}

	// change the authoritation, so that every one could read the file, but only admin could modify
	if err := obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
		return nil, nil, err
	}

	// get the url links, attrs.MediaLink
	attrs, err := obj.Attrs(ctx)
	fmt.Printf("Post is saved to GCS: %s\n", attrs.MediaLink)
	return obj, attrs, err
}

// Save a post to ElasticSearch
func saveToES(p *Post, id string) {
	// Create a client
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Save it to index
	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE).
		Id(id).
		BodyJson(p).
		Refresh(true).
		Do()
	if err != nil {
		panic(err)
		return
	}

	fmt.Printf("Post is saved to Index: %s\n", p.Message)
}

func handlerSearch(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one request for search")
	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	// range is optional
	ran := DISTANCE
	if val := r.URL.Query().Get("range"); val != "" {
		ran = val + "km"
	}

	fmt.Printf("Search received: %f %f %s\n", lat, lon, ran)

	// we run the elasticsearch server on the google cloud GAE instance
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false)) // setsniff 是否使用回调函数，查看/记录log
	if err != nil {
		panic(err)
		return
	}

	// Define geo distance query as specified in
	// https://www.elastic.co/guide/en/elasticsearch/reference/5.2/query-dsl-geo-distance-query.html
	q := elastic.NewGeoDistanceQuery("location")
	q = q.Distance(ran).Lat(lat).Lon(lon)

	// Some delay may range from seconds to minutes. So if you don't get enough results. Try it later.
	searchResult, err := client.Search().
		Index(INDEX).
		Query(q).
		Pretty(true).
		Do()

	if err != nil {
		// Handle error
		panic(err)
	}

	// searchResult is of type SearchResult and returns hits, suggestions,
	// and all kinds of other information from Elasticsearch.
	fmt.Printf("Query took %d milliseconds\n", searchResult.TookInMillis)
	// TotalHits is another convenience function that works even when something goes wrong.
	fmt.Printf("Found a total of %d post\n", searchResult.TotalHits())

	// Each is a convenience function that iterates over hits in a search result.
	// It makes sure you don't need to check for nil values in the response.
	// However, it ignores errors in serialization.
	var typ Post
	var ps []Post
	for _, item := range searchResult.Each(reflect.TypeOf(typ)) { // instance of // reflect 函数代表告诉each函数，将searchResult的object里面的item，只返回可以转化成Post类型的结果
		p := item.(Post) // p = (Post) item
		fmt.Printf("Post by %s: %s at lat %v and lon %v\n", p.User, p.Message, p.Location.Lat, p.Location.Lon)
		// TODO(student homework): Perform filtering based on keywords such as web spam etc.
		ps = append(ps, p)

	}
	js, err := json.Marshal(ps)
	if err != nil {
		panic(err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(js)

}
