package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/ericlys/goReact/internal/api"
	"github.com/ericlys/goReact/internal/store/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		panic(err)
	}

	ctx := context.Background()
	poll, err := pgxpool.New(ctx, fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s",
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_NAME")))

	if err != nil {
		panic(err)
	}

	defer poll.Close()

	if err := poll.Ping(ctx); err != nil {
		panic(err)
	}

	handler := api.NewHandler(pgstore.New(poll))

	go func() {
		if err := http.ListenAndServe(":8080", handler); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				panic(err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
}
