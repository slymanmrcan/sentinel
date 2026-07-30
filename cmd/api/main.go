package main

import (
	"context"
	"log"

	"github.com/slymanmrcan/sentinel/internal/app"
	"github.com/slymanmrcan/sentinel/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := application.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
