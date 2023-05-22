# smol-dev-go

Go implementation of [smol developer](https://github.com/smol-ai/developer)


## Installation

Prerequisites:
* Go (`brew install go`)

```shell
$ go install github.com/tmc/smol-dev-go@main
```

### Options
```shell
$ smol-dev-go -h

Usage of smol-dev-go:
  -concurrency int
    	number of concurrent files to generate (default 5)
  -model string
    	model to use (default "gpt-4")
  -prompt string
    	prompt to use (can be a filename)
  -target-dir string
    	target directory to write files to
  -verbose
    	verbose output
```

### Example Usage

Suppose you want to generate a small app that prints "Hello World" in different languages based on user input. You can run the following command:

```shell
smol-dev-go -prompt="the app prints 'Hello World' in different languages based on user input" -target-dir="./generated_code" -verbose=true
```
This command will generate the necessary files in the ./generated_code directory.
