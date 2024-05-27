// Command smol-dev-go is a program that utilizes an AI model to generate complete code projects based on user prompts. The program takes user input, processes it, and produces a list of file paths, shared dependencies, and actual code for those files, all of which are stored in a specified target directory.
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
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

var (
	flagPrompt          = flag.String("prompt", "", "prompt to use (can be a filename)")
	flagModel           = flag.String("model", "gpt-4o", "model to use")
	flagTargetDir       = flag.String("target-dir", "", "target directory to write files to")
	flagConcurrency     = flag.Int("concurrency", 5, "number of concurrent files to generate")
	flagVerbose         = flag.Bool("verbose", false, "verbose output")
	flagDebug           = flag.Bool("debug", false, "debug output (show prompts)")
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

	return generateFiles(prompt, filesToGenerate, string(sharedDepsYaml))
}

func generateFiles(prompt string, filesToGenerate []string, sharedDepsYaml string) error {
	g := new(errgroup.Group)
	g.SetLimit(*flagConcurrency)

	progressBars := mpb.New()
	for i, fp := range filesToGenerate {
		fp := pathInTargetDir(fp)

		if fileExists(fp) {
			fmt.Printf("file %v already exists, skipping\n", fp)
			continue
		}

		g.Go(func() error {
			return generateFile(prompt, fp, sharedDepsYaml, filesToGenerate, i, len(filesToGenerate), progressBars)
		})
		time.Sleep(time.Millisecond)
	}

	err := g.Wait()
	progressBars.Wait()
	return err
}

func generateFile(prompt, fp, sharedDepsYaml string, filesToGenerate []string, i, total int, progressBars *mpb.Progress) error {
	msg := fmt.Sprintf("generating file %v of %v: %v", i+1, total, fp)
	bar := progressBars.AddBar(1, mpb.PrependDecorators(
		decor.Name(msg),
	), mpb.AppendDecorators(
		decor.OnComplete(decor.Spinner(nil), "✅"),
	), mpb.BarNoPop())

	defer bar.SetCurrent(1)
	fmt.Println(msg)

	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return fmt.Errorf("failed to create directory %v: %w", filepath.Dir(fp), err)
	}

	return runCodeGenLLMCall(prompt, msg, fp, sharedDepsYaml, filesToGenerate)
}

func getFilesToGenerate(prompt, flagFilesToGenerate string) ([]string, error) {
	if flagFilesToGenerate != "" && fileExistsAndNonEmpty(flagFilesToGenerate) {
		return readStringSliceFromYaml(flagFilesToGenerate)
	}

	filePathsResult, err := runFilePathsLLMCall(prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to run file paths LLM call: %w", err)
	}

	if *flagVerbose {
		printYAML("files to generate:", filePathsResult.Filepaths)
	}

	if flagFilesToGenerate != "" {
		if err := writeYAML(flagFilesToGenerate, filePathsResult.Filepaths); err != nil {
			return nil, fmt.Errorf("failed to write files to generate file: %w", err)
		}
	}

	return filePathsResult.Filepaths, nil
}

type SharedDependenciesLLMResponse struct {
	SharedDependencies []sharedDependency `json:"shared_dependencies"`
	Reasoning          []string           `json:"reasoning"`
}

type sharedDependency struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Symbols     map[string]string `json:"symbols"`
}

func getSharedDependencies(prompt string, filesToGenerate []string, flagSharedDeps string) (*SharedDependenciesLLMResponse, error) {
	if flagSharedDeps != "" && fileExistsAndNonEmpty(flagSharedDeps) {
		return readSharedDependenciesFromYaml(flagSharedDeps)
	}

	sharedDepsResult, err := runSharedDependenciesLLMCall(prompt, filesToGenerate)
	if err != nil {
		return nil, fmt.Errorf("failed to run shared dependencies LLM call: %w", err)
	}

	if *flagVerbose {
		printYAML("shared dependencies:", sharedDepsResult.SharedDependencies)
	}

	if flagSharedDeps != "" {
		if err := writeYAML(flagSharedDeps, sharedDepsResult.SharedDependencies); err != nil {
			return nil, fmt.Errorf("failed to write shared deps file: %w", err)
		}
	}

	return sharedDepsResult, nil
}

func readPrompt() (string, error) {
	if *flagPrompt == "" {
		return "", fmt.Errorf("no prompt specified")
	}

	if fileExists(*flagPrompt) {
		return readFile(*flagPrompt)
	}

	return *flagPrompt, nil
}

type filepathLLMResponse struct {
	Filepaths []string `json:"filepaths"`
	Reasoning []string `json:"reasoning"`
}

func runFilePathsLLMCall(prompt string) (*filepathLLMResponse, error) {
	if *flagVerbose {
		fmt.Println("running file paths LLM call")
	} else {
		defer spin("generating file list", "finished generating file list")()
	}

	ctx := context.Background()
	llm, err := openai.New(openai.WithModel(*flagModel))
	if err != nil {
		return nil, fmt.Errorf("failed to create llm: %w", err)
	}

	if *flagDebug {
		fmt.Println("debug mode enabled, dumping prompt")
		fmt.Println(filesPathsPrompt)
		fmt.Println(prompt)
	}

	cr, err := llm.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, prompt),
		llms.TextParts(llms.ChatMessageTypeHuman, filesPathsPrompt),
	}, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		fmt.Fprint(os.Stderr, string(chunk))
		return nil
	}))

	if err != nil {
		return nil, fmt.Errorf("failed to chat: %w", err)
	}

	result := &filepathLLMResponse{}
	if err = json.Unmarshal(findJSON(cr.Choices[0].Content), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, cr.Choices[0].Content)
	}

	return result, nil
}

func runSharedDependenciesLLMCall(prompt string, filePaths []string) (*SharedDependenciesLLMResponse, error) {
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
	llm, err := openai.New(openai.WithModel(*flagModel))
	if err != nil {
		return nil, fmt.Errorf("failed to create llm: %w", err)
	}

	inputs := map[string]interface{}{
		"prompt":           prompt,
		"filepaths_string": filePaths,
		"target_json": emptyJSON(&SharedDependenciesLLMResponse{
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

	parts := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, sharedDependenciesPrompt),
	}

	generation, err := llm.GenerateContent(ctx, parts, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		fmt.Fprint(os.Stderr, string(chunk))
		return nil
	}))

	if err != nil {
		return nil, fmt.Errorf("failed to get llm result: %w", err)
	}

	result := &SharedDependenciesLLMResponse{}
	if err = json.Unmarshal(findJSON(generation.Choices[0].Content), result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w\nRaw output: %v", err, generation.Choices[0].Content)
	}

	return result, nil
}

func runCodeGenLLMCall(prompt, msg, file, sharedDeps string, filePaths []string) error {
	ctx := context.Background()
	spt := prompts.NewPromptTemplate(codeGenerationSystemPrompt, []string{"prompt", "filepaths_string", "shared_dependencies"})
	pt := prompts.NewPromptTemplate(codeGenerationPrompt, []string{"prompt", "filepaths_string", "shared_dependencies", "filename"})
	llm, err := openai.New(openai.WithModel(*flagModel))
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

	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file %v: %w", file, err)
	}
	defer f.Close()

	_, err = llm.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, genPrompt),
	}, llms.WithModel(*flagModel), llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		if _, err := f.Write(chunk); err != nil {
			return fmt.Errorf("failed to write to file %v: %w", file, err)
		}
		return f.Sync()
	}))

	return err
}

func pathInTargetDir(path string) string {
	if *flagTargetDir != "" {
		if err := os.MkdirAll(*flagTargetDir, 0755); err != nil {
			panic(fmt.Errorf("failed to create target directory %v: %w", *flagTargetDir, err))
		}
	}
	return filepath.Join(*flagTargetDir, path)
}

func readStringSliceFromYaml(path string) ([]string, error) {
	var result []string
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()
	return result, yaml.NewDecoder(f).Decode(&result)
}

func readSharedDependenciesFromYaml(path string) (*SharedDependenciesLLMResponse, error) {
	result := &SharedDependenciesLLMResponse{}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open shared deps file: %w", err)
	}
	defer f.Close()
	return result, yaml.NewDecoder(f).Decode(result)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileExistsAndNonEmpty(path string) bool {
	if !fileExists(path) {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return string(b), nil
}

func writeYAML(path string, data interface{}) error {
	y, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}
	return os.WriteFile(path, y, 0644)
}

func printYAML(title string, data interface{}) {
	y, _ := yaml.Marshal(data)
	fmt.Println(title)
	fmt.Println(string(y))
}

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

Don't generate package lock files for any language.

Your response must be JSON formatted and contain the following keys:
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

Your response must be JSON formatted and contain the following keys:
"shared_dependencies": a the list of shared dependencies, include a symbol name, a description, and the set of symbols or files. use "name", "description", and "symbols" as the keys.
"reasoning": a list of strings that explain your chain of thought (include 5-10).
The symbols should be a map of symbol name to symbol description. ("symbols": {"(symbol_name)": "(symbol_description)"})

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
