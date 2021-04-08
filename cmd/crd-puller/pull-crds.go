package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/MakeNowJust/heredoc"
	crdpuller "github.com/kcp-dev/kcp/pkg/crdpuller"
	"github.com/muesli/reflow/wordwrap"
	"github.com/spf13/cobra"
	terminal "github.com/wayneashleyberry/terminal-dimensions"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

var reEmptyLine = regexp.MustCompile(`(?m)([\w[:punct:]])[ ]*\n([\w[:punct:]])`)

func Helpdoc(s string) string {
	s = heredoc.Doc(s)
	s = reEmptyLine.ReplaceAllString(s, "$1 $2")
	return s
}

func main() {
	cobra.AddTemplateFunc("trimTrailingWhitespaces", func(s string) string {
		w, err := terminal.Width()
		if err != nil {
			w = 80
		}
		return strings.TrimRightFunc(wordwrap.String(s, int(w)), unicode.IsSpace)
	})

	cmd := &cobra.Command{
		Use:        "pull-crds",
		Aliases:    []string{},
		SuggestFor: []string{},
		Short:      "Pull CRDs from a Kubernetes cluster",
		Long: Helpdoc(`
					Pull CRDs from a Kubernetes cluster
					Based on a kubeconfig file, it uses dicovery API and the OpenAPI v2
					model on the cluster to build CRDs for a list of api resource names.
				`),
		Example: "",
		RunE: func(cmd *cobra.Command, args []string) error {
			kubeconfigPath := cmd.Flag("kubeconfig").Value.String()
			config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
			if err != nil {
				return err
			}
			puller, err := crdpuller.NewSchemaPuller(config)
			if err != nil {
				return err
			}
			crds, err := puller.PullCRDs(context.TODO(), args...)
			if err != nil {
				return err
			}
			for name, crd := range crds {
				yamlBytes, err := yaml.Marshal(crd)
				if err != nil {
					return err
				}

				ioutil.WriteFile(name+".yaml", []byte(yamlBytes), os.ModePerm)
			}
			return nil
		},
	}
	cmd.Flags().String("kubeconfig", ".kubeconfig", "kubeconfig file used to contact the cluster.")

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
