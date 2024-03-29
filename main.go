// Command smol-dev-go is a software program that utilizes an AI model to generate complete code projects based on user prompts. The program takes user input, processes it, and produces a list of file paths, shared dependencies, and actual code for those files, all of which are stored in a specified target directory.
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
	"gopkg.in/yaml.v3"

	"github.com/tmc/langchaingo/llms"
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
	flagDebug       = flag.Bool("debug", false, "debug output (show prompts)")

	flagFilesToGenerate = flag.String("files-to-generate", "", "file path to a yaml file containing a list of files to generate")
	flagSharedDeps      = flag.String("shared-deps", "", "file path to a yaml file containing a list of shared dependencies")
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

	filesToGenerate, err := getFilesToGenerate(prompt, *flagFilesToGenerate)
	if err != nil {
		return fmt.Errorf("failed to get files to generate: %w", err)
	}

	sharedDeps, err := getSharedDependencies(prompt, filesToGenerate, *flagSharedDeps)
	if err != nil {
		return fmt.Errorf("failed to get shared dependencies: %w", err)
	}
	sharedDepsYaml, err := yaml.Marshal(sharedDeps)
	if err != nil {
		return fmt.Errorf("failed to marshal shared dependencies: %w", err)
	}

	g := new(errgroup.Group)
	g.SetLimit(*flagConcurrency)
	// generate all files:

	progressBars := mpb.New()
	for i, fp := range filesToGenerate {
		i := i
		fp := pathInTargetDir(fp)

		// check if already exists:
		if _, err := os.Stat(fp); err == nil {
			fmt.Printf("file %v already exists, skipping\n", fp)
			continue
		}
		g.Go(func() error {
			msg := fmt.Sprintf("generating file %v of %v: %v", i+1, len(filesToGenerate), fp)
			bar := progressBars.AddBar(1, mpb.PrependDecorators(
				decor.Name(msg),
			),
				mpb.AppendDecorators(
					decor.OnComplete(decor.Spinner(nil), "✅"),
				),
				mpb.BarNoPop(),
			)
			defer bar.SetCurrent(1)
			fmt.Println(msg)

			// ensure directory exists:
			if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
				return fmt.Errorf("failed to create directory %v: %w", filepath.Dir(fp), err)
			}
			// call codegen LLM:
			err := runCodeGenLLMCall(prompt, msg, fp, string(sharedDepsYaml), filesToGenerate)
			if err != nil {
				return fmt.Errorf("failed to run codegen LLM call for %v: %w", fp, err)
			}
			return nil
		})
		time.Sleep(time.Millisecond)
	}
	err = g.Wait()
	progressBars.Wait()
	return err
}

func getFilesToGenerate(prompt string, flagFilesToGenerate string) ([]string, error) {
	var result []string
	var err error

	if flagFilesToGenerate != "" && existsAndNonEmpty(flagFilesToGenerate) {
		result, err = readStringSliceFromYaml(flagFilesToGenerate)
		if err != nil {
			return nil, fmt.Errorf("failed to read files to generate from file: %w", err)
		}
	} else {
		filePathsResult, err := runFilePathsLLMCall(prompt)
		if err != nil {
			return nil, fmt.Errorf("failed to run file paths LLM call: %w", err)
		}
		result = filePathsResult.Filepaths
	}
	y, _ := yaml.Marshal(result)
	if *flagVerbose {
		fmt.Println("files to generate:")
		fmt.Println(string(y))
	}
	if flagFilesToGenerate != "" {
		if err := os.WriteFile(flagFilesToGenerate, y, 0644); err != nil {
			return nil, fmt.Errorf("failed to write files to generate file: %w", err)
		}
	}
	return result, nil
}

func existsAndNonEmpty(fp string) bool {
	if _, err := os.Stat(fp); err != nil {
		return false
	}
	if fi, err := os.Stat(fp); err == nil && fi.Size() == 0 {
		return false
	}
	return true
}

func getSharedDependencies(prompt string, filesToGenerate []string, flagSharedDeps string) ([]sharedDependency, error) {
	var result []sharedDependency
	var err error

	if flagSharedDeps != "" && existsAndNonEmpty(flagSharedDeps) {
		result, err = readSharedDependenciesFromYaml(flagSharedDeps)
		if err != nil {
			return nil, fmt.Errorf("failed to read shared dependencies from yaml: %w", err)
		}
	} else {
		sharedDepsResult, err := runSharedDependenciesLLMCall(prompt, filesToGenerate)
		if err != nil {
			return nil, fmt.Errorf("failed to run shared dependencies LLM call: %w", err)
		}
		result = sharedDepsResult.SharedDependencies
	}
	y, _ := yaml.Marshal(result)
	if *flagVerbose {
		fmt.Println("shared dependencies:")
		fmt.Println(string(y))
	}
	if flagSharedDeps != "" {
		if err := os.WriteFile(flagSharedDeps, y, 0644); err != nil {
			return nil, fmt.Errorf("failed to write shared deps file: %w", err)
		}
	}
	return result, nil
}

func readSharedDependenciesFromYaml(path string) ([]sharedDependency, error) {
	result := []sharedDependency{}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open shared deps file: %w", err)
	}
	return result, yaml.NewDecoder(f).Decode(&result)

}

type filepathLLMResponse struct {
	Filepaths []string `json:"filepaths"`
	Reasoning []string `json:"reasoning"`
}

func runFilePathsLLMCall(prompt string) (*filepathLLMResponse, error) {
	defer fmt.Println()
	if *flagVerbose {
		fmt.Println("running file paths LLM call")
	} else {
		defer spin("generating file list", "finished generating file list")()
	}
	ctx := context.Background()
	llm, err := openai.NewChat(openai.WithModel(*flagModel))
	if err != nil {
		return nil, fmt.Errorf("failed to create llm: %w", err)
	}
	if *flagDebug {
		fmt.Println("debug mode enabled, dumping prompt")
		fmt.Println(filesPathsPrompt)
		fmt.Println(prompt)
	}
	cr, err := llm.Call(ctx, []schema.ChatMessage{
		&schema.SystemChatMessage{Content: filesPathsPrompt},
		&schema.HumanChatMessage{Content: prompt},
	}, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		fmt.Fprint(os.Stderr, string(chunk))
		return nil
	}))

	if err != nil {
		return nil, fmt.Errorf("failed to chat: %w", err)
	}
	result := &filepathLLMResponse{}
	if err = json.Unmarshal(findJSON(cr.Content), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, cr.Content)
	}
	return result, nil
}

type sharedDependenciesLLMResponse struct {
	SharedDependencies []sharedDependency `json:"shared_dependencies"`
	Reasoning          []string           `json:"reasoning"`
}

type sharedDependency struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Symbols     map[string]string `json:"symbols"`
}

func runSharedDependenciesLLMCall(prompt string, filePaths []string) (*sharedDependenciesLLMResponse, error) {
	defer fmt.Println()
	if *flagVerbose {
		fmt.Println("running file paths LLM call")
	} else {
		defer spin("generate dependencies list", "finished generating")()
	}
	ctx := context.Background()
	pt := prompts.NewPromptTemplate(sharedDependenciesPrompt, []string{
		"prompt", "filepaths_string",
		"target_json",
	})
	llm, err := openai.NewChat(openai.WithModel(*flagModel))
	if err != nil {
		return nil, fmt.Errorf("failed to create llm: %w", err)
	}
	inputs := map[string]interface{}{
		"prompt":           prompt,
		"filepaths_string": filePaths,
		"target_json": emptyJSON(&sharedDependenciesLLMResponse{
			Reasoning: []string{},
			SharedDependencies: []sharedDependency{
				{
					Name:        "example symbol",
					Description: "example description",
				},
			},
		}),
	}
	systemPrompt, err := pt.Format(inputs)
	if err != nil {
		return nil, fmt.Errorf("failed to format prompt: %w", err)
	}
	fmt.Println(systemPrompt)
	generation, err := llm.Call(ctx, []schema.ChatMessage{
		&schema.SystemChatMessage{Content: systemPrompt},
	}, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		fmt.Fprint(os.Stderr, string(chunk))
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to get llm result: %w", err)
	}
	result := &sharedDependenciesLLMResponse{}
	if err = json.Unmarshal(findJSON(generation.Content), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, generation.Content)
	}
	return result, nil
}

func runCodeGenLLMCall(prompt, msg, file, sharedDeps string, filePaths []string) error {
	//defer spin(msg, "wrote files")()
	ctx := context.Background()
	spt := prompts.NewPromptTemplate(codeGenerationSystemPrompt, []string{"prompt", "filepaths_string", "shared_dependencies"})
	pt := prompts.NewPromptTemplate(codeGenerationPrompt, []string{"prompt", "filepaths_string", "shared_dependencies", "filename"})
	llm, err := openai.NewChat(openai.WithModel(*flagModel))
	if err != nil {
		return fmt.Errorf("failed to create llm: %w", err)
	}
	inputs := map[string]interface{}{
		"prompt":              prompt,
		"filepaths_string":    filePaths,
		"shared_dependencies": sharedDeps,
		"filename":            file,
	}
	systemPrompt, err := spt.Format(inputs)
	if err != nil {
		return fmt.Errorf("failed to format system prompt: %w", err)
	}
	genPrompt, err := pt.Format(inputs)
	if err != nil {
		return fmt.Errorf("failed to format prompt: %w", err)
	}

	// open file for writing:
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file %v: %w", file, err)
	}
	defer f.Close()
	_, err = llm.Call(ctx, []schema.ChatMessage{
		&schema.SystemChatMessage{Content: systemPrompt},
		&schema.HumanChatMessage{Content: genPrompt},
	}, llms.WithModel(*flagModel), llms.WithStreamingFunc(
		// Stream writes to file:
		func(ctx context.Context, chunk []byte) error {
			if _, err := f.Write(chunk); err != nil {
				return fmt.Errorf("failed to write to file %v: %w", file, err)
			}
			return f.Sync()
		}))
	return err
}

func pathInTargetDir(path string) string {
	// ensure target dir exists:
	if *flagTargetDir != "" {
		if err := os.MkdirAll(*flagTargetDir, 0755); err != nil {
			panic(fmt.Errorf("failed to create target directory %v: %w", *flagTargetDir, err))
		}
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

func readStringSliceFromYaml(path string) ([]string, error) {
	var result []string
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return result, yaml.NewDecoder(f).Decode(&result)
}

// extracts a json string from a string
func findJSON(s string) []byte {
	re := regexp.MustCompile(`(?s)\{.*\}`)
	return re.Find([]byte(s))
}

func emptyJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const filesPathsPrompt = `
You are an AI developer who is trying to write a program that will generate code for the user based on their intent.

When given their intent, create a complete, exhaustive list of filepaths that the user would write to make the program. You should include a Makefile and a Dockerfile.

Your repsonse must be JSON formatted and contain the following keys:
"filepaths": a list of strings that are the filepaths that the user would write to make the program.
"reasoning": a list of strings that explain your chain of thought (include 5-10)

Do not emit any other output.`

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
"shared_dependencies": a the list of shared dependencies, include a symbol name, a description, and the set of symbols or files. use "name", "description", and "symbols" as the keys.
"reasoning": a list of strings that explain your chain of thought (include 5-10).
The symbols should be a map of symbol name to symbol description.

Your output should be JSON should look like:
{{.target_json}}

Do not emit any other output.`

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
   - you are generating code for the file {{.filename}}
   - do not stray from the names of the files and the shared dependencies we have decided on
   - MOST IMPORTANT OF ALL - the purpose of our app is {{.prompt}} - every line of code you generate must be valid code. Do not include code fences in your response, for example

Bad response:
` + "```" + `javascript
console.log("hello world")
` + "```" + `

Good response:
console.log("hello world")

Begin generating the specified file now (with surrounding text):
`
