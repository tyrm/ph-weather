package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/jsonapi"
)

type Config struct {
	HTTPPort             string

	RedisAddr            string
	RedisDB              int
	RedisPassword        string
	RedisPrefix          string

	WUndergroundKey      string
	WUndergroundLocation string
}

type Env struct {
	config *Config
	redis  *redis.Client
}

type SunPhaseRespose struct {
	ResponseID string `jsonapi:"primary,sun_phase"`
	SunriseM   int    `jsonapi:"attr,sunrise_m"`
	SunriseH   int    `jsonapi:"attr,sunrise_h"`
	SunsetM    int    `jsonapi:"attr,sunset_m"`
	SunsetH    int    `jsonapi:"attr,sunset_h"`
}

type WUAstronomy struct {
	Response  json.RawMessage `json:"response"`
	MoonPhase json.RawMessage `json:"moon_phase"`
	SunPhase  WUSunPhase      `json:"sun_phase"`
}

type WUSunPhase struct {
	Sunrise WUTime `json:"sunrise"`
	Sunset  WUTime `json:"sunset"`
}

type WUTime struct {
	Hour   string `json:"hour"`
	Minute string `json:"minute"`
}

func collectConfig() (config Config) {
	var missingEnv []string

	// REDIS_DB
	var envHTTPPort string = os.Getenv("HTTP_PORT")

	if envHTTPPort == "" {
		config.HTTPPort = ":8080"
	} else {
		config.HTTPPort = string(":" + envHTTPPort)
	}

	// REDIS_ADDR
	config.RedisAddr = os.Getenv("REDIS_ADDR")
	if config.RedisAddr == "" {
		missingEnv = append(missingEnv, "REDIS_ADDR")
	}

	// REDIS_PASSWORD
	config.RedisPassword = os.Getenv("REDIS_PASSWORD")

	// REDIS_DB
	var envRedisDB string = os.Getenv("REDIS_DB")

	if envRedisDB == "" {
		config.RedisDB = 0
	} else {
		i, err := strconv.Atoi(envRedisDB)
		panicOnError(err, "Error parsing REDIS_DB")
		config.RedisDB = i
	}

	// REDIS_PREFIX
	var envRedisPrefix string = os.Getenv("REDIS_PREFIX")

	if envRedisPrefix == "" {
		config.RedisPrefix = "ph:"
	} else {
		config.RedisPrefix = envRedisPrefix
	}

	// WU_KEY
	config.WUndergroundKey = os.Getenv("WU_KEY")
	if config.WUndergroundKey == "" {
		missingEnv = append(missingEnv, "WU_KEY")
	}

	// WU_LOCATION
	config.WUndergroundLocation = os.Getenv("WU_LOCATION")
	if config.WUndergroundLocation == "" {
		missingEnv = append(missingEnv, "WU_LOCATION")
	}

	// Validation
	if len(missingEnv) > 0 {
		var msg string = fmt.Sprintf("Environment variables missing: %v", missingEnv)
		log.Fatal(msg)
		panic(fmt.Sprint(msg))
	}

	return
}

func getWUApiRepose(key string, feature string, location string) (resString string, resError error) {
	url := string("https://api.wunderground.com/api/" + key + "/" + feature + "/q/" + location + ".json")
	response, err := http.Get(url)
	if err != nil {
		resError = err
		log.Fatal(err)
		return
	}
	defer response.Body.Close()

	responseData, err := ioutil.ReadAll(response.Body)
	if err != nil {
		resError = err
		log.Fatal(err)
		return
	}

	resString = string(responseData)
	return
}

func getWUAstronomy(key string, feature string, location string) (response WUAstronomy, resError error) {
	astronomy, err := getWUApiRepose(key, feature, location)
	if err != nil {
		resError = err
		return
	}
	json.Unmarshal([]byte(astronomy), &response)
	return
}

func (env *Env) handleSunPhase(response http.ResponseWriter, request *http.Request) {
	if request.Method == "GET" {
		today := time.Now()
		cacheKey := fmt.Sprintf("%sweather:sun_phase:%d-%s-%d", env.config.RedisPrefix, today.Year(), today.Month(), today.Day())

		cacheVal, err := env.redis.Get(cacheKey).Result()
		if err != nil && err != redis.Nil {
			log.Println("Error reading cache: %s", err)
		} else if err == nil {
			// Send response'
			response.Header().Set("Content-Type", jsonapi.MediaType)
			fmt.Fprint(response, cacheVal)
			return
		}

		astronomy, err := getWUAstronomy(env.config.WUndergroundKey,"astronomy", env.config.WUndergroundLocation)
		if err != nil {
			makeErrorResponse(response, 500, err.Error(), 0)
		}

		SunriseH, err := strconv.Atoi(astronomy.SunPhase.Sunrise.Hour)
		SunriseM, err := strconv.Atoi(astronomy.SunPhase.Sunrise.Minute)
		SunsetH, err := strconv.Atoi(astronomy.SunPhase.Sunset.Hour)
		SunsetM, err := strconv.Atoi(astronomy.SunPhase.Sunset.Minute)

		responseObj := &SunPhaseRespose{ResponseID: cacheKey, SunriseH: SunriseH, SunriseM: SunriseM,
			SunsetH: SunsetH, SunsetM: SunsetM}

		// Build Response
		var eventPayload bytes.Buffer
		if err := jsonapi.MarshalPayload(&eventPayload, responseObj); err != nil {
			makeErrorResponse(response, 500, err.Error(), 0)
			return
		}

		// Cache event for Pollers
		var ttl time.Duration = time.Duration(168) * time.Hour
		cacheErr := env.redis.Set(cacheKey, eventPayload.String(), ttl).Err()
		if cacheErr != nil {
			log.Println("Error commiting to cache: %s", cacheErr)
		}

		// Send response
		response.Header().Set("Content-Type", jsonapi.MediaType)
		fmt.Fprint(response, eventPayload.String())
		return
	} else {
		makeErrorResponse(response, 405, request.Method, 0)
		return
	}
}

func makeErrorResponse(response http.ResponseWriter, status int, detail string, code int) {
	var codeTitle map[int]string
	codeTitle = make(map[int]string)
	codeTitle[1] = "Malformed JSON Body"
	codeTitle[2201] = "Missing Required Attribute"
	codeTitle[2202] = "Requested Relationship Not Found"

	var statusTitle map[int]string
	statusTitle = make(map[int]string)
	statusTitle[400] = "Bad Request"
	statusTitle[401] = "Unauthorized"
	statusTitle[404] = "Not Found"
	statusTitle[405] = "Method Not Allowed"
	statusTitle[406] = "Not Acceptable"
	statusTitle[409] = "Conflict"
	statusTitle[415] = "Unsupported Media Type"
	statusTitle[422] = "Unprocessable Entity"
	statusTitle[500] = "Internal Server Error"

	var title string
	var statusStr string = strconv.Itoa(status)
	var codeStr string

	// Get Title
	if code == 0 { // code 0 means no code
		title = statusTitle[status]
	} else {
		title = codeTitle[code]
		codeStr = strconv.Itoa(code)
	}

	// Send Response
	response.WriteHeader(status)
	response.Header().Set("Content-Type", jsonapi.MediaType)
	jsonapi.MarshalErrors(response, []*jsonapi.ErrorObject{{
		Title:  title,
		Detail: detail,
		Status: statusStr,
		Code:   codeStr,
	}})

	return
}

func panicOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}

func main() {
	config := collectConfig()

	// Connect to Redis
	client := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword, // no password set
		DB:       config.RedisDB,       // use default DB
	})

	pong, err := client.Ping().Result()
	log.Printf("redis ping: %s", pong)
	panicOnError(err, "Failed to connect to Redis")
	log.Println("Connected to Redis")

	// Build Environment
	env := &Env{redis: client, config: &config}

	http.HandleFunc("/weather/sun_phase/v1", env.handleSunPhase)
	http.ListenAndServe(config.HTTPPort, nil)

	log.Println("Ready")
}