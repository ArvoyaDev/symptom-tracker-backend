package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/ArvoyaDev/symptom-tracker-backend/internal/auth"
	db "github.com/ArvoyaDev/symptom-tracker-backend/internal/mysql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"golang.org/x/time/rate"
)

type config struct {
	dbName         string
	dataSourceName string
	AuthClient     *auth.CognitoClient
	dbClientData   db.DBClientData
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	dataSourceName := os.Getenv("AWS_DATABASE_URL")
	port := os.Getenv("PORT")
	dbName := os.Getenv("DATABASE_NAME")
	authClient := auth.Init()
	clientData := db.DBClientData{
		AwsRegion:   os.Getenv("AWS_REGION"),
		DbName:      os.Getenv("DATABASE_NAME"),
		DbUser:      os.Getenv("DATABASE_USER"),
		RdsEndpoint: os.Getenv("RDS_ENDPOINT"),
	}
	config := config{
		dbName:         dbName,
		dataSourceName: dataSourceName,
		AuthClient:     authClient,
		dbClientData:   clientData,
	}

	// Main router with subrouting
	mainMux := http.NewServeMux()

	// DB Mux & routes
	dbMux := http.NewServeMux()
	mainMux.HandleFunc("/testdb", config.testDBConnection)

	// dbMux.HandleFunc("GET /logs", config.getHeartburnLogs)
	// dbMux.HandleFunc("POST /logs", config.createHeartburnLog)

	authMux := TokenAuthMiddleware(dbMux)

	mainMux.Handle("/db/", http.StripPrefix("/db", authMux))

	// Cognito Mux & routes
	cognitoMux := http.NewServeMux()

	mainMux.Handle("/aws-cognito/", http.StripPrefix("/aws-cognito", cognitoMux))

	cognitoMux.HandleFunc("POST /signup", config.signUp)
	cognitoMux.HandleFunc("POST /confirm-signup", config.ConfirmSignup)
	cognitoMux.HandleFunc("POST /request-verification-code", config.RequestVerificationCode)
	cognitoMux.HandleFunc("POST /sign-in", config.SignIn)

	// Apply CORS middleware
	corsMux := corsMiddleware(mainMux)

	// Apply rate limiter middleware
	rateLimitMux := rateLimitMiddleware(corsMux)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: rateLimitMux,
	}
	log.Printf("Server listening on port %s", port)
	log.Fatal(srv.ListenAndServe())
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set the Access-Control-Allow-Origin header to allow all origins
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// Set the Access-Control-Allow-Methods header to allow all methods
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		// Set the Access-Control-Allow-Headers header to allow all headers
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func rateLimitMiddleware(next http.Handler) http.HandlerFunc {
	// Set the rate limit to 15 requests per second with a burst of 5 request
	limiter := rate.NewLimiter(rate.Limit(15), 5)

	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	}
}

func TokenAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header missing", http.StatusUnauthorized)
			return
		}

		splitAuthHeader := strings.Split(authHeader, " ")
		if len(splitAuthHeader) != 2 || splitAuthHeader[0] != "Bearer" {
			http.Error(w, "Invalid authorization header", http.StatusBadRequest)
			return
		}

		// Fetch JWK set
		keySet, err := jwk.Fetch(r.Context(), os.Getenv("AWS_TOKEN_SIGNING_KEY"))
		if err != nil {
			http.Error(w, "Error fetching keys", http.StatusInternalServerError)
			return
		}

		// Parse and validate token
		token, err := jwt.Parse(
			[]byte(splitAuthHeader[1]),
			jwt.WithKeySet(keySet),
			jwt.WithValidate(true),
		)
		if err != nil {
			http.Error(w, "Error parsing token", http.StatusBadRequest)
			return
		}

		// Validate the token
		if err := jwt.Validate(token); err != nil {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Token is valid, proceed with the next handler
		next.ServeHTTP(w, r)
	})
}
