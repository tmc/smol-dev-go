package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/prompts"
	"github.com/tmc/langchaingo/schema"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

var (
	flagPrompt      = flag.String("prompt", "", "prompt to use (can be a filename)")
	flagModel       = flag.String("model", "gpt-4", "model to use")
	flagTargetDir   = flag.String("target-dir", "", "target directory to write files to")
	flagConcurrency = flag.Int("concurrency", 5, "number of concurrent files to generate")
	flagVerbose     = flag.Bool("verbose", false, "verbose output")
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

	if *flagVerbose {
		y, _ := json.MarshalIndent(filePathsResult, "", "  ")
		fmt.Println(string(y))
	}

	sharedDeps, err := runSharedDependenciesLLMCall(prompt, filePathsResult.Filepaths)
	if err != nil {
		return err
	}

	sharedDepsYaml, err := json.MarshalIndent(sharedDeps, "", "  ")
	if err := os.WriteFile(pathInTargetDir("shared_dependencies.md"), sharedDepsYaml, 0644); err != nil {
		return fmt.Errorf("failed to write shared dependencies: %w", err)
	}
	if *flagVerbose {
		fmt.Println(string(sharedDepsYaml))
	}

	g := new(errgroup.Group)
	g.SetLimit(*flagConcurrency)
	// generate all files:

	progressBars := mpb.New()
	for i, fp := range filePathsResult.Filepaths {
		i := i
		fp := pathInTargetDir(fp)
		g.Go(func() error {
			msg := fmt.Sprintf("generating file %v of %v: %v", i+1, len(filePathsResult.Filepaths), fp)
			bar := progressBars.AddBar(1, mpb.PrependDecorators(
				decor.Name(msg),
			),
				mpb.AppendDecorators(
					decor.OnComplete(decor.Spinner(nil), "✅"),
				),
				mpb.BarNoPop(),
			)
			defer bar.SetCurrent(1)

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
		time.Sleep(time.Millisecond)
	}
	progressBars.Wait()
	return g.Wait()
}

type filepathLLMResponse struct {
	Reasoning []string `json:"reasoning"`
	Filepaths []string `json:"filepaths"`
}

func runFilePathsLLMCall(prompt string) (*filepathLLMResponse, error) {
	defer fmt.Println()
	defer spin("generating file list", "finished generating file list")()
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
	if err = json.Unmarshal(findJSON(cr.Message.Text), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, cr.Message.Text)
	}
	return result, nil
}

type sharedDependenciesLLMResponse struct {
	Reasoning          []string `json:"reasoning"`
	SharedDependencies []string `json:"shared_dependencies"`
}

func runSharedDependenciesLLMCall(prompt string, filePaths []string) (*sharedDependenciesLLMResponse, error) {
	defer fmt.Println()
	defer spin("generate dependencies list", "finished generating")()
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
	if err = json.Unmarshal(findJSON(generation.Message.Text), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, generation.Message.Text)
	}
	return result, nil
}

func runCodeGenLLMCall(prompt, msg, file, sharedDeps string, filePaths []string) (string, error) {
	//defer spin(msg, "wrote files")()
	ctx := context.Background()
	spt := prompts.NewPromptTemplate(codeGenerationSystemPrompt, []string{"prompt", "filepaths_string", "shared_dependencies"})
	pt := prompts.NewPromptTemplate(codeGenerationPrompt, []string{"prompt", "filepaths_string", "shared_dependencies", "file_path"})
	llm, err := openai.New()
	if err != nil {
		return "", fmt.Errorf("failed to create llm: %w", err)
	}
	inputs := map[string]interface{}{
		"prompt":              prompt,
		"filepaths_string":    filePaths,
		"shared_dependencies": sharedDeps,
		"file_path":           file,
	}
	systemPrompt, err := spt.Format(inputs)
	if err != nil {
		return "", fmt.Errorf("failed to format system prompt: %w", err)
	}
	genPrompt, err := pt.Format(inputs)
	generation, err := llm.Chat(ctx, []schema.ChatMessage{
		&schema.SystemChatMessage{Text: systemPrompt},
		&schema.HumanChatMessage{Text: genPrompt},
	})
	if err != nil {
		return "", fmt.Errorf("failed to chat: %w", err)
	}
	return generation.Message.Text, nil
}

func pathInTargetDir(path string) string {
	// ensure target dir exists:
	if err := os.MkdirAll(*flagTargetDir, 0755); err != nil {
		panic(fmt.Errorf("failed to create target directory %v: %w", *flagTargetDir, err))
	}
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

// extracts a json string from a string
func findJSON(s string) []byte {
	re := regexp.MustCompile(`(?s)\{.*\}`)
	return re.Find([]byte(s))
}

const filesPathsPrompt = `
You are an AI developer who is trying to write a program that will generate code for the user based on their intent.

When given their intent, create a complete, exhaustive list of filepaths that the user would write to make the program. You should include a Makefile and a Dockerfile.

Your repsonse must be JSON formatted and contain the following keys:
"reasoning": a list of strings that explain your chain of thought (include 5-10)
"filepaths": a list of strings that are the filepaths that the user would write to make the program.

Do not emit any other output.
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
"shared_dependencies": a list of strings that are the filepaths that the user would write to make the program.

Do not emit any other output.
`

const codeGenerationSystemPrompt = `
You are an AI developer who is trying to write a program that will generate code for the user based on their intent.

the app is: {{.prompt}}

the files we have decided to generate are: {{ toJson .filepaths_string}}

the shared dependencies (like filenames and variable names) we have decided on are: {{.shared_dependencies}}

only write valid code for the given filepath and file type, and return only the code.
do not add any other explanation, only return valid code for that file type.`

const codeGenerationPrompt = `
We have broken up the program into per-file generation.
Now your job is to generate only the code for the file {{.filename}}.
Make sure to have consistent filenames if you reference other files we are also generating.

Remember that you must obey 3 things:
   - you are generating code for the file {{filename}}
   - do not stray from the names of the files and the shared dependencies we have decided on
   - MOST IMPORTANT OF ALL - the purpose of our app is {{.prompt}} - every line of code you generate must be valid code. Do not include code fences in your response, for example

Bad response:
` + "```" + `javascript
console.log("hello world")
` + "```" + `

Good response:
console.log("hello world")

Begin generating the code now.
`
