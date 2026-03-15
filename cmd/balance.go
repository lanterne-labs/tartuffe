package cmd

import (
	"context"
	"digital-mirror/backend/alerting"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var balanceCmd = &cobra.Command{
	Use:   "balance",
	Short: "LLM provider balance commands",
}

var balanceCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check LLM provider balances",
	Long:  "Queries each configured LLM provider for remaining credits and prints a summary.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		checkers := buildBalanceCheckers()
		if len(checkers) == 0 {
			fmt.Println("No LLM providers detected (OPEN_AI_API does not contain openrouter.ai)")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROVIDER\tBALANCE\tLIMIT\tFREE TIER\tSTATUS")
		fmt.Fprintln(w, "--------\t-------\t-----\t---------\t------")

		var lowBalanceInfos []*alerting.BalanceInfo
		threshold := getBalanceAlertThreshold()

		for _, checker := range checkers {
			info, err := checker.CheckBalance(ctx)
			if err != nil {
				fmt.Fprintf(w, "%s\t-\t-\t-\t%s\n", checker.ProviderName(), err.Error())
				continue
			}

			status := "OK"
			if info.Limit > 0 && info.Balance < threshold {
				status = "LOW"
				lowBalanceInfos = append(lowBalanceInfos, info)
			}
			if info.IsFreeTier {
				status = "FREE"
			}

			fmt.Fprintf(w, "%s\t$%.2f\t$%.2f\t%v\t%s\n",
				info.Provider, info.Balance, info.Limit, info.IsFreeTier, status)
		}
		w.Flush()

		// If --alert flag is set and there are low balances, send Telegram alert
		sendAlert, _ := cmd.Flags().GetBool("alert")
		if sendAlert && len(lowBalanceInfos) > 0 {
			notifier := alerting.NewTelegramNotifierFromEnv()
			if notifier == nil {
				fmt.Println("\n--alert requested but TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID not set")
				return nil
			}
			for _, info := range lowBalanceInfos {
				msg := fmt.Sprintf(
					"<b>Low LLM Balance</b>\nProvider: %s\nBalance: $%.2f\nLimit: $%.2f\nThreshold: $%.2f",
					info.Provider, info.Balance, info.Limit, threshold,
				)
				if err := notifier.Send(ctx, msg); err != nil {
					log.Error().Err(err).Str("provider", info.Provider).Msg("Failed to send Telegram alert")
				} else {
					fmt.Printf("\nTelegram alert sent for %s\n", info.Provider)
				}
			}
		}

		return nil
	},
}

func init() {
	balanceCmd.AddCommand(balanceCheckCmd)
	rootCmd.AddCommand(balanceCmd)

	balanceCheckCmd.Flags().Bool("alert", false, "Send Telegram alert if balance is low")
}

// buildBalanceCheckers creates all available BalanceChecker instances from env vars.
func buildBalanceCheckers() []alerting.BalanceChecker {
	var checkers []alerting.BalanceChecker
	if c := alerting.NewOpenRouterCheckerFromEnv(); c != nil {
		checkers = append(checkers, c)
	}
	return checkers
}

// buildAlerter creates an Alerter with all available checkers and Telegram notifier.
// Returns nil if no checkers or notifier are available.
func buildAlerter() *alerting.Alerter {
	checkers := buildBalanceCheckers()
	notifier := alerting.NewTelegramNotifierFromEnv()
	threshold := getBalanceAlertThreshold()
	cooldown := getBalanceAlertCooldown()
	return alerting.NewAlerter(checkers, notifier, threshold, cooldown)
}

func getBalanceAlertThreshold() float64 {
	if v := os.Getenv("BALANCE_ALERT_THRESHOLD_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 2.0
}

func getBalanceAlertCooldown() time.Duration {
	if v := os.Getenv("BALANCE_ALERT_COOLDOWN_HOURS"); v != "" {
		if h, err := strconv.ParseFloat(v, 64); err == nil && h > 0 {
			return time.Duration(h * float64(time.Hour))
		}
	}
	return 6 * time.Hour
}

func getBalanceCheckInterval() time.Duration {
	if v := os.Getenv("BALANCE_CHECK_INTERVAL_MIN"); v != "" {
		if m, err := strconv.ParseFloat(v, 64); err == nil && m > 0 {
			return time.Duration(m * float64(time.Minute))
		}
	}
	return 60 * time.Minute
}
