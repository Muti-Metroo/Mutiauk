package cli

import (
	"os"

	"github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "completion <bash|zsh|fish|powershell>",
		Short:                 "Generate shell completion scripts",
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Long: `Emit a shell-completion script to stdout. Source it from your
shell, or install system-wide:

  bash:
    mutiauk completion bash | sudo tee /etc/bash_completion.d/mutiauk

  zsh:
    mutiauk completion zsh > "${fpath[1]}/_mutiauk"

  fish:
    mutiauk completion fish > ~/.config/fish/completions/mutiauk.fish

  powershell:
    mutiauk completion powershell > mutiauk.ps1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return nil
		},
	}
	return cmd
}
