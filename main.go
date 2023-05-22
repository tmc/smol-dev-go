package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/prompts"
	"github.com/tmc/langchaingo/schema"
)

var (
	flagPrompt      = flag.String("prompt", "", "prompt to use (can be a filename)")
	flagModel       = flag.String("model", "gpt-4", "model to use")
	flagTargetDir   = flag.String("target-dir", "", "target directory to write files to")
	flagConcurrency = flag.Int("concurrency", 4, "number of concurrent files to generate")
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()
	prompt, err := readPrompt()
	if err != nil {
		return err
	}
	filePathsResult, err := runFilePathsLLMCall(prompt)
	if err != nil {
		return err
	}

	sharedDeps, err := runSharedDependenciesLLMCall(prompt, filePathsResult.Filepaths)
	if err != nil {
		return err
	}
	sharedDepsYaml, err := json.MarshalIndent(sharedDeps, "", "  ")
	if err := os.WriteFile(pathInTargetDir("shared_dependencies.md"), sharedDepsYaml, 0644); err != nil {
		return fmt.Errorf("failed to write shared dependencies: %w", err)
	}

	g := new(errgroup.Group)
	g.SetLimit(*flagConcurrency)

	// generate all files:
	for i, fp := range filePathsResult.Filepaths {
		i := i
		fp := pathInTargetDir(fp)
		g.Go(func() error {
			msg := fmt.Sprintf("Generating file %v of %v: %v", i+1, len(filePathsResult.Filepaths), fp)

			// call codegen LLM:
			src, err := runCodeGenLLMCall(prompt, msg, fp, string(sharedDepsYaml), filePathsResult.Filepaths)
			if err != nil {
				return fmt.Errorf("failed to run codegen LLM call for %v: %w", fp, err)
			}
			// ensure directory exists:
			if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
				return fmt.Errorf("failed to create directory %v: %w", filepath.Dir(fp), err)
			}
			// write file:
			if err := os.WriteFile(fp, []byte(src), 0644); err != nil {
				return fmt.Errorf("failed to write file %v: %w", fp, err)
			}
			return nil
		})
	}
	return g.Wait()
}

type filepathLLMResponse struct {
	Reasoning []string `json:"reasoning"`
	Filepaths []string `json:"filepaths"`
}

func runFilePathsLLMCall(prompt string) (*filepathLLMResponse, error) {
	defer spin("generating file paths")()
	ctx := context.Background()
	//pt := prompts.NewPromptTemplate(filesPathsPrompt, []string{"prompt"})
	llm, err := openai.New(openai.WithModel(*flagModel))
	if err != nil {
		return nil, fmt.Errorf("failed to create llm: %w", err)
	}
	cr, err := llm.Chat(ctx, []schema.ChatMessage{
		&schema.SystemChatMessage{Text: filesPathsPrompt},
		&schema.HumanChatMessage{Text: prompt},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to chat: %w", err)
	}
	result := &filepathLLMResponse{}
	if err = json.Unmarshal([]byte(cr.Message.Text), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, cr.Message.Text)
	}
	return result, nil
}

type sharedDependenciesLLMResponse struct {
	Reasoning          []string `json:"reasoning"`
	SharedDependencies []string `json:"shared_dependencies"`
}

func runSharedDependenciesLLMCall(prompt string, filePaths []string) (*sharedDependenciesLLMResponse, error) {
	defer spin("generate dependencies list")()
	ctx := context.Background()
	pt := prompts.NewPromptTemplate(sharedDependenciesPrompt, []string{"prompt", "filepaths_string"})
	llm, err := openai.New(openai.WithModel(*flagModel))
	if err != nil {
		return nil, fmt.Errorf("failed to create llm: %w", err)
	}
	inputs := map[string]interface{}{
		"prompt":           prompt,
		"filepaths_string": filePaths,
	}
	systemPrompt, err := pt.Format(inputs)
	if err != nil {
		return nil, fmt.Errorf("failed to format prompt: %w", err)
	}
	generation, err := llm.Chat(ctx, []schema.ChatMessage{
		&schema.SystemChatMessage{Text: systemPrompt},
	})
	result := &sharedDependenciesLLMResponse{}
	if err = json.Unmarshal([]byte(generation.Message.Text), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, generation.Message.Text)
	}
	return result, nil
}

func runCodeGenLLMCall(prompt, msg, file, sharedDeps string, filePaths []string) (string, error) {
	defer spin(msg)()
	ctx := context.Background()
	pt := prompts.NewPromptTemplate(codeGenerationPrompt, []string{"prompt", "filepaths_string", "shared_dependencies"})
	llm, err := openai.New()
	if err != nil {
		return "", fmt.Errorf("failed to create llm: %w", err)
	}
	smolDevGo := chains.NewLLMChain(llm, pt)
	inputs := map[string]interface{}{
		"prompt":              "smol-dev-go: a go program to assist with program development",
		"filepaths_string":    filePaths,
		"shared_dependencies": sharedDeps,
	}
	result, err := chains.Call(ctx, smolDevGo, inputs)
	return result["text"].(string), err
}

func pathInTargetDir(path string) string {
	return filepath.Join(*flagTargetDir, path)
}

func readPrompt() (string, error) {
	if *flagPrompt == "" {
		return "", fmt.Errorf("no prompt specified")
	}
	// if it's a file path then read the contents
	if _, err := os.Stat(*flagPrompt); err == nil {
		b, err := os.ReadFile(*flagPrompt)
		if err != nil {
			return "", fmt.Errorf("failed to read prompt file: %w", err)
		}
		return string(b), nil
	}
	return *flagPrompt, nil
}

const filesPathsPrompt = `
You are an AI developer who is trying to write a program that will generate code for the user based on their intent.

Tips: include a Makefile and a Dockerfile

When given their intent, create a complete, exhaustive list of filepaths that the user would write to make the program.

Your repsonse must be JSON formatted and contain the following keys:
"reasoning": a list of strings that explain your chain of thought (include 5-10)
"filepaths": a list of strings that are the filepaths that the user would write to make the program
`

const sharedDependenciesPrompt = `
You are an AI developer who is trying to write a program that will generate code for the user based on their intent.

In response to the user's prompt:

---
the app is: {{.prompt}}
---

the files we have decided to generate are: {{ toJson .filepaths_string}}

Now that we have a list of files, we need to understand what dependencies they share.
Please name and briefly describe what is shared between the files we are generating, including exported variables, data schemas, id names of every DOM elements that javascript functions will use, message names, and function names.

Your repsonse must be JSON formatted and contain the following keys:
"reasoning": a list of strings that explain your chain of thought (include 5-10)
"shared_dependencies": a list of strings that are the filepaths that the user would write to make the program
`

const codeGenerationPrompt = `
You are an AI developer who is trying to write a program that will generate code for the user based on their intent.

the app is: {{.prompt}}

the files we have decided to generate are: {{ toJson .filepaths_string}}

the shared dependencies (like filenames and variable names) we have decided on are: {{.shared_dependencies}}

only write valid code for the given filepath and file type, and return only the code.
do not add any other explanation, only return valid code for that file type.`
