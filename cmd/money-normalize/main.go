package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
)

func main() {
	configPath := flag.String("config", "config.yaml", "bot configuration file")
	report := flag.Bool("report", false, "print the captured per-deployment money audit")
	apply := flag.Bool("apply", false, "apply the explicit money-unit decision once")
	unit := flag.String("unit", "", "historical numeric unit: toman or rial")
	operator := flag.String("operator", "", "name or ticket identifying the operator making this decision")
	confirm := flag.String("confirm", "", "exact confirmation token printed by -report")
	flag.Parse()

	if !*report && !*apply {
		fmt.Fprintln(os.Stderr, "choose -report or -apply; see -h for details")
		os.Exit(2)
	}
	if err := config.Load(*configPath); err != nil {
		log.Fatalf("load configuration: %v", err)
	}
	ctx := context.Background()
	if err := db.Connect(ctx, &config.Global.Database); err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer db.Pool.Close()
	if err := db.Migrate(ctx); err != nil {
		log.Fatalf("run additive migrations: %v", err)
	}

	var state *db.MoneyNormalizationState
	var err error
	if *apply {
		state, err = db.NormalizeLegacyMoney(ctx, *unit, *operator, *confirm)
		if err != nil {
			log.Fatalf("apply money normalization: %v", err)
		}
		fmt.Printf("Money normalization applied: unit=%s operator=%s database=%s at=%s\n", state.SelectedUnit, state.NormalizedBy, state.DatabaseName, state.NormalizedAt.UTC().Format(time.RFC3339))
	}
	if *report {
		state, err = db.GetMoneyNormalizationState(ctx)
		if err != nil {
			log.Fatalf("read money audit: %v", err)
		}
		encoded, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			log.Fatalf("format money audit: %v", err)
		}
		fmt.Println(string(encoded))
		if state.NormalizedAt == nil {
			fmt.Fprintln(os.Stderr, "Review this report, choose whether historical numeric amounts were Toman or Rial, then apply once with -apply -unit <toman|rial> -operator <name-or-ticket> -confirm <confirmation-token>.")
		}
	}
}
