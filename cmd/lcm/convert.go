package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
)

// convert rewrites lcm/v1 documents as lcm/v2 without a server, so that a
// Git config source can be converted in one commit before upgrading.
func (a *app) convert(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	var files multiFlag
	fs.Var(&files, "f", "file or directory of lcm/v1 documents; repeatable, \"-\" reads standard input")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("convert: unexpected argument %q", pos[0])
	}
	if len(files) == 0 {
		return usagef("convert: at least one -f <file|dir> is required")
	}
	docs, err := a.readDocuments(files)
	if err != nil {
		return err
	}
	// Documents are converted per file so that problems name the position
	// of the document within its file.
	var out bytes.Buffer
	total, failed := 0, 0
	for start := 0; start < len(docs); {
		end := start
		for end < len(docs) && docs[end].Path == docs[start].Path {
			end++
		}
		path := docs[start].Path
		converted, warns, err := model.ConvertV1(joinDocuments(docs[start:end]))
		for _, w := range warns {
			_, _ = fmt.Fprintf(a.stderr, "%s: warning: %s\n", path, w)
		}
		if err != nil {
			_, _ = fmt.Fprintf(a.stderr, "%s: %v\n", path, err)
		}
		total += end - start
		failed += end - start - len(converted)
		for _, doc := range converted {
			y, err := model.EncodeYAML(doc)
			if err != nil {
				return err
			}
			if out.Len() > 0 {
				out.WriteString("---\n")
			}
			out.Write(y)
		}
		start = end
	}
	if _, err := a.stdout.Write(out.Bytes()); err != nil {
		return err
	}
	if failed > 0 {
		return problemError{fmt.Sprintf("%d of %d documents could not be converted", failed, total)}
	}
	return nil
}
