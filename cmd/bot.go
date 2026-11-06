package cmd

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/xbot"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var botCmd = &cobra.Command{
	Use:   "bot",
	Short: "The X bot: its drafts and the accounts it tags.",
	Long: `The X bot tweets the contradictions of the candidates (backend/xbot). Each tweet
is written by the LLM in a few versions, tags the person, one of their parties,
the outlets that published the facts and other accounts of the list, and waits
for a person's approval. "dm bot draft" shows drafts without saving or sending
anything; "dm bot accounts" keeps the list of accounts to tag.`,
}

var (
	botDraftN    int
	botDraftLink bool
	botDraftSite string
	botDraftID   int64
	botDraftTG   bool
)

var botDraftCmd = &cobra.Command{
	Use:   "draft",
	Short: "Show drafts of tweets, one per person, without saving or sending them.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		store, err := openBotStore(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		writer, err := xbot.NewLLMWriterFromEnv()
		if err != nil {
			return fmt.Errorf("init LLM: %w", err)
		}
		var tg *xbot.Telegram
		if botDraftTG {
			if tg, err = xbot.NewTelegramFromEnv(); err != nil {
				return err
			}
			if tg == nil {
				return fmt.Errorf("--telegram: TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are not set")
			}
		}
		var picked []storage.BotContradiction
		if botDraftID > 0 {
			c, err := store.GetBotContradiction(ctx, botDraftID)
			if err != nil {
				return err
			}
			picked = []storage.BotContradiction{*c}
		} else if picked, err = store.PickBotContradictions(ctx, botDraftN, nil); err != nil {
			return err
		}
		for _, c := range picked {
			pool, err := store.BotTagPool(ctx, &c, xbot.Domains(&c))
			if err != nil {
				return err
			}
			link := ""
			if botDraftLink {
				link = xbot.ContradictionLink(botDraftSite, &c)
			}
			d, err := xbot.Compose(ctx, writer, &c, xbot.ChooseTags(pool, xbot.MaxTags), link)
			if err != nil {
				return err
			}
			printDraft(d)
			if tg != nil {
				msg := "🔎 <b>APERÇU</b>, rien ne sera publié\n\n" + xbot.DraftMessage(d, time.Now(), botDraftSite)
				if _, err := tg.Send(ctx, msg, nil); err != nil {
					return fmt.Errorf("send the preview: %w", err)
				}
			}
		}
		fmt.Printf("%d draft(s); nothing saved or sent\n", len(picked))
		return nil
	},
}

func printDraft(d *xbot.Draft) {
	c := d.Contradiction
	reviewed := "not reviewed"
	if c.Reviewed {
		reviewed = "reviewed real"
	}
	day := func(f storage.BotFact) string {
		if f.Date == nil {
			return "?"
		}
		return f.Date.Format("2006-01-02")
	}
	fmt.Printf("\n=== contradiction %d · %s · %s · %s\n", c.ID, c.PersonName, c.StanceLabel, reviewed)
	fmt.Printf("A (%s): %s\n", day(c.A), c.A.Text)
	fmt.Printf("B (%s): %s\n", day(c.B), c.B.Text)
	fmt.Printf("why: %s\n", c.Reason)
	fmt.Printf("tags: %s\n", strings.Join(d.Tags, " "))
	for i, v := range d.Versions {
		fmt.Printf("--- version %d (%d chars)\n%s\n", i+1, xbot.Length(v), v)
	}
	for _, why := range d.Dropped {
		fmt.Printf("--- dropped: %s\n", why)
	}
}

var botAccountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "The accounts the bot may tag: list, add, import, approve, refuse.",
}

var botAccountsStatus string

var botAccountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the accounts (--status suggested|approved|refused, default all).",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		store, err := openBotStore(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		accounts, err := store.ListXAccounts(ctx, botAccountsStatus)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			extra := ""
			if a.Domain != nil {
				extra += " site=" + *a.Domain
			}
			if a.PersonID != nil {
				extra += fmt.Sprintf(" person=%d", *a.PersonID)
			}
			if a.LastTaggedAt != nil {
				extra += " tagged=" + a.LastTaggedAt.Format("2006-01-02")
			}
			fmt.Printf("@%-20s %-11s %-9s %s%s %s\n", a.Handle, a.Category, a.Status, a.Name, extra, a.Note)
		}
		fmt.Printf("%d account(s)\n", len(accounts))
		return nil
	},
}

var botAccount struct {
	name, category, domain, person, status, note string
}

var botAccountsAddCmd = &cobra.Command{
	Use:   "add <handle>",
	Short: "Add an account (suggested unless --status), or change the fields given.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		store, err := openBotStore(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		a, err := xAccountFrom(ctx, store, args[0], botAccount.name, botAccount.category, botAccount.domain, botAccount.person, botAccount.status, botAccount.note)
		if err != nil {
			return err
		}
		saved, err := store.UpsertXAccount(ctx, a)
		if err != nil {
			return err
		}
		fmt.Printf("@%s %s %s\n", saved.Handle, saved.Category, saved.Status)
		return nil
	},
}

var botAccountsImportCmd = &cobra.Command{
	Use:   "import <file.csv>",
	Short: "Add or update accounts from a CSV: handle,name,category,domain,person,status,note (a header line first).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		store, err := openBotStore(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		r := csv.NewReader(f)
		r.FieldsPerRecord = 7
		r.Comment = '#'
		if _, err := r.Read(); err != nil {
			return fmt.Errorf("read the header: %w", err)
		}
		n := 0
		for {
			rec, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			a, err := xAccountFrom(ctx, store, rec[0], rec[1], rec[2], rec[3], rec[4], rec[5], rec[6])
			if err != nil {
				return err
			}
			if _, err := store.UpsertXAccount(ctx, a); err != nil {
				return err
			}
			n++
		}
		fmt.Printf("%d account(s) imported\n", n)
		return nil
	},
}

func botStatusCmd(status string) *cobra.Command {
	verb := map[string]string{"approved": "approve", "refused": "refuse", "suggested": "suggest"}[status]
	return &cobra.Command{
		Use:   verb + " <handle>...",
		Short: "Set accounts " + status + ".",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			store, err := openBotStore(ctx)
			if err != nil {
				return err
			}
			defer store.Close()
			n, err := store.SetXAccountStatus(ctx, status, args)
			if err != nil {
				return err
			}
			fmt.Printf("%d of %d account(s) %s\n", n, len(args), status)
			return nil
		},
	}
}

// xAccountFrom builds an account from text fields; person is a tracked
// person's name (exact match), "" for none.
func xAccountFrom(ctx context.Context, store *storage.PostgresStorer, handle, name, category, domain, person, status, note string) (storage.XAccount, error) {
	a := storage.XAccount{Handle: handle, Name: strings.TrimSpace(name), Category: strings.TrimSpace(category), Status: strings.TrimSpace(status), Note: strings.TrimSpace(note)}
	if a.Category != "" && !slices.Contains(storage.XAccountCategories, a.Category) {
		return a, fmt.Errorf("@%s: category %q is not one of %s", handle, a.Category, strings.Join(storage.XAccountCategories, ", "))
	}
	if a.Status != "" && !slices.Contains(storage.XAccountStatuses, a.Status) {
		return a, fmt.Errorf("@%s: status %q is not one of %s", handle, a.Status, strings.Join(storage.XAccountStatuses, ", "))
	}
	if d := strings.TrimSpace(domain); d != "" {
		a.Domain = &d
	}
	if p := strings.TrimSpace(person); p != "" {
		rec, err := core.ResolvePersonID(ctx, store, p, core.ExactMatchOnly)
		if err != nil {
			return a, fmt.Errorf("@%s: person %q: %w", handle, p, err)
		}
		a.PersonID = &rec.ID
	}
	return a, nil
}

func openBotStore(ctx context.Context) (*storage.PostgresStorer, error) {
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	store, err := deps.OpenStorer(ctx)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}
	return store, nil
}

func init() {
	botDraftCmd.Flags().IntVar(&botDraftN, "n", 5, "how many drafts (one per person)")
	botDraftCmd.Flags().Int64Var(&botDraftID, "contradiction", 0, "draft this contradiction instead of choosing")
	botDraftCmd.Flags().BoolVar(&botDraftLink, "link", false, "add the contradiction's link (X bills a tweet with a link more)")
	botDraftCmd.Flags().BoolVar(&botDraftTG, "telegram", false, "also send each draft to Telegram as a preview (no buttons, nothing saved)")
	botDraftCmd.Flags().StringVar(&botDraftSite, "site", "https://tartuffe.me", "the site of the links")
	botAccountsListCmd.Flags().StringVar(&botAccountsStatus, "status", "", "suggested, approved or refused (default all)")
	botAccountsAddCmd.Flags().StringVar(&botAccount.name, "name", "", "display name")
	botAccountsAddCmd.Flags().StringVar(&botAccount.category, "category", "", strings.Join(storage.XAccountCategories, ", "))
	botAccountsAddCmd.Flags().StringVar(&botAccount.domain, "domain", "", "the site it publishes (an outlet: lemonde.fr)")
	botAccountsAddCmd.Flags().StringVar(&botAccount.person, "person", "", "the tracked person or party it is the account of")
	botAccountsAddCmd.Flags().StringVar(&botAccount.status, "status", "", "suggested (default for a new one), approved or refused")
	botAccountsAddCmd.Flags().StringVar(&botAccount.note, "note", "", "a note")
	botAccountsCmd.AddCommand(botAccountsListCmd, botAccountsAddCmd, botAccountsImportCmd,
		botStatusCmd("approved"), botStatusCmd("refused"), botStatusCmd("suggested"))
	botCmd.AddCommand(botDraftCmd, botAccountsCmd)
	rootCmd.AddCommand(botCmd)
}
