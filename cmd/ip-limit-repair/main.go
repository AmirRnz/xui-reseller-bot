package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
)

func main() {
	configPath := flag.String("config", "config.yaml", "bot configuration file")
	dryRun := flag.Bool("dry-run", false, "show candidate rows without changing data")
	apply := flag.Bool("apply", false, "apply the reviewed one-time repair")
	factor := flag.Int("factor", 0, "historical multiplier to divide by, such as 100")
	operator := flag.String("operator", "", "name or ticket identifying the operator")
	confirm := flag.String("confirm", "", "exact preview token printed by -dry-run")
	flag.Parse()
	if *dryRun == *apply {
		fmt.Fprintln(os.Stderr, "choose exactly one of -dry-run or -apply")
		os.Exit(2)
	}
	if *factor <= 1 {
		fmt.Fprintln(os.Stderr, "-factor must be greater than 1")
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
	var preview *db.IPLimitRepairPreview
	var err error
	if *dryRun {
		preview, err = db.PreviewLegacyIPLimitRepair(ctx, *factor)
	} else {
		preview, err = db.ApplyLegacyIPLimitRepair(ctx, *factor, *operator, *confirm)
	}
	if err != nil {
		log.Fatalf("IP-limit repair: %v", err)
	}
	encoded, err := json.MarshalIndent(preview, "", "  ")
	if err != nil {
		log.Fatalf("format IP-limit report: %v", err)
	}
	fmt.Println(string(encoded))
	if *dryRun {
		fmt.Fprintln(os.Stderr, "Review affected and excluded rows. Apply once only with -apply -factor <same-factor> -operator <name-or-ticket> -confirm <preview-token>.")
	}
}
