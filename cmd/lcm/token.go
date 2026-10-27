package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
)

func (a *app) token(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("token: expected subcommand create, list or revoke")
	}
	sub, rest := args[0], args[1:]
	fs, cfg, err := a.newFlagSet("token " + sub)
	if err != nil {
		return err
	}
	var name *string
	switch sub {
	case "create":
		name = fs.String("name", "", "token name (required)")
	case "list", "revoke":
	default:
		return usagef("token: unknown subcommand %q", sub)
	}
	ctx, db, err := a.openStore(ctx, fs, cfg, rest)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	svc, err := auth.NewService(db, cfg.SessionTTL)
	if err != nil {
		return err
	}
	log := audit.New(db)

	switch sub {
	case "create":
		if fs.NArg() > 0 {
			return usagef("token create: unexpected argument %q", fs.Arg(0))
		}
		if *name == "" {
			return usagef("token create: --name is required")
		}
		tok, secret, err := svc.CreateToken(ctx, *name)
		if err != nil {
			return err
		}
		if err := log.Record(ctx, "token.create", "token:"+strconv.FormatInt(tok.ID, 10), audit.OutcomeOK,
			map[string]any{"name": tok.Name}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(a.stderr, "Token %d (%s) created. Store it now; it cannot be shown again.\n", tok.ID, tok.Name)
		_, err = fmt.Fprintln(a.stdout, secret)
		return err

	case "list":
		if fs.NArg() > 0 {
			return usagef("token list: unexpected argument %q", fs.Arg(0))
		}
		tokens, err := svc.ListTokens(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tCREATED\tLAST USED\tSTATUS")
		for _, t := range tokens {
			status := "active"
			if t.RevokedAt != nil {
				status = "revoked"
			}
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", t.ID, t.Name, fmtTime(&t.CreatedAt), fmtTime(t.LastUsedAt), status)
		}
		return tw.Flush()

	default: // revoke
		if fs.NArg() != 1 {
			return usagef("token revoke: expected exactly one token id")
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil || id <= 0 {
			return usagef("token revoke: invalid token id %q", fs.Arg(0))
		}
		target := "token:" + strconv.FormatInt(id, 10)
		if err := svc.RevokeToken(ctx, id); err != nil {
			if errors.Is(err, auth.ErrNotFound) {
				return fmt.Errorf("token %d not found", id)
			}
			recordCLI(ctx, db, "token.revoke", target, audit.OutcomeError, nil)
			return err
		}
		if err := log.Record(ctx, "token.revoke", target, audit.OutcomeOK, nil); err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.stdout, "Token %d revoked.\n", id)
		return err
	}
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
