package cache

import (
	"net/http"
	"time"
	"os"
	"github.com/Mixelito/prerender/render"
	"github.com/pkg/errors"

	"github.com/go-redis/redis"


	"github.com/minio/minio-go"
	"log"
	"bytes"
	"strings"
)

type RedisCache struct {
	client *redis.Client
}

type S3Cache struct {
	client *minio.Client
	bucket string
}

// Cache caches prerendering results for quick retrieval later
type Cache interface {
	Check(*http.Request) (*render.Result, error)
	Save(*render.Result, time.Duration) error
}

var storeType = os.Getenv("CACHE")

/*
// NewCache creates a new caching layer using Redis as backend
func NewCache(client *redis.Client) Cache {
	return &redisCache{client}
}
*/

/*
//here I want to get bool if my value implements Somether
    _, ok := val.(Somether)
    //but val must be interface, hm..what if I want explicit type?

    //yes, here is another method:
    var _ Iface = (*MyType)(nil)
 */
// NewCache creates a new caching layer using Redis as backend
func NewCache() Cache {
	if storeType=="redis" {

		redisAddr := os.Getenv("REDIS_URL")
		if redisAddr == "" {
			redisAddr = "redis://localhost:6379/0"
		}
		opts, err := redis.ParseURL(redisAddr)
		if err != nil {
			log.Fatal("error parsing redis url", err)
		}
		client := redis.NewClient(opts)
		defer client.Close()

		return &RedisCache{client}
	} else if storeType=="s3" {
		awsAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
		awsSecret := os.Getenv("AWS_SECRET_ACCESS_KEY")
		awsBucket := os.Getenv("AWS_S3_BUCKET_NAME")
		awsRegion := os.Getenv("AWS_REGION")
		if awsRegion=="" {
			awsRegion = "us-east-1"
		}

		client, err := minio.NewWithRegion("s3.amazonaws.com", awsAccessKey, awsSecret, true, awsRegion)

		if err != nil {
			log.Fatal("error authenticate aws s3", err)
		}

		return &S3Cache{client,awsBucket}
	}
	return nil
}

func (c *RedisCache) checkEtag(r *http.Request) (bool, error) {
	if etag := r.Header.Get("If-None-Match"); etag != "" {
		redisEtag, err := c.client.HGet(r.URL.Path, "Etag").Result()
		if err != nil && err != redis.Nil {
			return false, errors.Wrap(err, "getting cached etag failed")
		}
		return etag == redisEtag, nil
	}
	return false, nil
}

func (c *RedisCache) Check(r *http.Request) (*render.Result, error) {
	matches, err := c.checkEtag(r)
	if err != nil {
		return nil, err
	}
	if matches {
		return &render.Result{Status: http.StatusNotModified}, nil
	}

	data, err := c.client.HGetAll(r.URL.String()).Result()
	if err != nil {
		return nil, errors.Wrap(err, "getting cached data failed")
	}
	html, ok := data["html"]
	if !ok {
		return nil, nil
	}

	res := render.Result{
		Status: http.StatusOK,
		HTML:   html,
		Etag:   data["Etag"],
	}
	return &res, nil
}

func (c *RedisCache) Save(res *render.Result, ttl time.Duration) error {
	tx := c.client.TxPipeline()
	tx.HSet(res.URL, "Etag", res.Etag)
	tx.HSet(res.URL, "html", res.HTML)
	tx.PExpire(res.URL, ttl)

	_, err := tx.Exec()
	return err
}

func (c *S3Cache) Check(r *http.Request) (*render.Result, error) {
	log.Printf("CHECK %s", r.URL.String())
	reader, err := c.client.GetObject(c.bucket, r.URL.String())
	defer reader.Close()

	//info
	//info, err := c.client.StatObject(c.bucket, r.URL.String())
	//log.Printf("INFO %s", info)

	if err != nil {
		return nil, nil
	}

	buf := new(bytes.Buffer)
	buf.ReadFrom(reader)
	html := buf.String()

	if buf.Len() == 0  {
		return nil, nil
	}

	res := render.Result{
		Status: http.StatusOK,
		HTML:   html,
		//Etag:   data["Etag"],
	}

	return &res, err
}

func (c *S3Cache) Save(res *render.Result, ttl time.Duration) error {

	log.Printf("SAVE %s", res.URL)
	reader := strings.NewReader(res.HTML)

	metadata := map[string][]string{
		"Content-Type": []string{"text/html"},
		"Etag": []string{res.Etag},
		"StorageClass": []string{"REDUCED_REDUNDANCY"},
	}
	log.Printf("METADATA %s", metadata)

	n, err := c.client.PutObjectWithMetadata(c.bucket, res.URL, reader, metadata, nil)
	_ = n
	if err != nil {
		log.Fatalln(err)
	}

	return err
}
