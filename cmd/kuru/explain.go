package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/explain"
)

func init() {
	var useLLM, exact bool
	cmd := &cobra.Command{
		Use:   "explain <run-dir>",
		Short: "Explain why a run failed, in plain English",
		Long: "Read a failing run (its -min run when kuru shrink made one) and write a short root-cause story: " +
			"which faults happened, who was leader, which operation broke which guarantee, and why, with step " +
			"numbers. The story is printed and saved as explain.md in the run folder, where the viewer shows it.\n\n" +
			"It is rule-based. With --llm the same facts are rewritten by Claude (ANTHROPIC_API_KEY) or Gemini " +
			"(GEMINI_API_KEY); without a key, or if the call fails, the rule-based story is kept.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := explain.Resolve(args[0])
			if exact {
				dir = filepath.Clean(args[0])
			}
			f, err := explain.Gather(dir)
			if err != nil {
				return fmt.Errorf("explain %s: %w", dir, err)
			}
			story := explain.Story(f)
			if useLLM {
				if p := explain.Provider(); p == "" {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warn: --llm needs ANTHROPIC_API_KEY or GEMINI_API_KEY; using the rule-based story")
				} else if s, err := explain.LLMStory(cmd.Context(), f, story); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warn: %v; using the rule-based story\n", err)
				} else {
					story = s + "\n_Rewritten by " + p + " from the rule-based facts._\n"
				}
			}
			if dir != filepath.Clean(args[0]) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "explaining the minimal run %s\n", dir)
			}
			if err := os.WriteFile(filepath.Join(dir, explain.File), []byte(story), 0o644); err != nil {
				return err
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), story)
			return nil
		},
	}
	cmd.Flags().BoolVar(&exact, "exact", false, "explain <run-dir> itself, even if kuru shrink made a -min run")
	cmd.Flags().BoolVar(&useLLM, "llm", false, "rewrite the story with Claude or Gemini (needs ANTHROPIC_API_KEY or GEMINI_API_KEY)")
	rootCmd.AddCommand(cmd)
}
