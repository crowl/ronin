package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crowl/ronin/workflow"
)

type workflowCommand struct {
	Script string
	Input  string
}

func parseWorkflowCommand(args []string, stdin io.Reader) (workflowCommand, bool, error) {
	if len(args) == 0 || args[0] != "run" {
		return workflowCommand{}, false, nil
	}

	command, err := parseWorkflowArgs(args[1:])
	if err != nil {
		return workflowCommand{}, true, err
	}
	if command.Script == "" {
		return workflowCommand{}, true, fmt.Errorf("usage: ronin run <script.lua> [<input> | --input <file> | -]")
	}
	if command.InputFile != "" {
		input, err := readWorkflowInputFile(command.InputFile)
		if err != nil {
			return workflowCommand{}, true, err
		}
		return workflowCommand{Script: command.Script, Input: input}, true, nil
	}
	if command.ReadStdin {
		if stdin == nil {
			return workflowCommand{}, true, fmt.Errorf("read workflow stdin: input is unavailable")
		}
		input, err := readWorkflowInput(stdin, "stdin")
		if err != nil {
			return workflowCommand{}, true, err
		}
		return workflowCommand{Script: command.Script, Input: input}, true, nil
	}
	return workflowCommand{Script: command.Script, Input: command.InlineInput}, true, nil
}

const maxWorkflowInputBytes int64 = 1 << 20

type workflowArgs struct {
	Script      string
	InlineInput string
	InputFile   string
	ReadStdin   bool
}

func parseWorkflowArgs(args []string) (workflowArgs, error) {
	var parsed workflowArgs
	var inlineInputSet bool
	var inputFileSet bool
	options := true

	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return workflowArgs{}, fmt.Errorf("usage: ronin run <script.lua> [<input> | --input <file> | -]")
	}
	parsed.Script = args[0]

	for i := 1; i < len(args); i++ {
		arg := args[i]

		if options && arg == "--" {
			if inlineInputSet || inputFileSet || parsed.ReadStdin || i+1 >= len(args) {
				return workflowArgs{}, fmt.Errorf("unexpected workflow argument %q", arg)
			}
			options = false
			continue
		}
		if options && arg == "--input" {
			if inlineInputSet || inputFileSet || parsed.ReadStdin {
				return workflowArgs{}, fmt.Errorf("workflow input sources conflict: use one of inline text, --input <file>, or -")
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return workflowArgs{}, fmt.Errorf("workflow --input requires a file path")
			}
			i++
			parsed.InputFile = args[i]
			if parsed.InputFile == "" {
				return workflowArgs{}, fmt.Errorf("workflow --input requires a file path")
			}
			inputFileSet = true
			continue
		}
		if options && strings.HasPrefix(arg, "--input=") {
			if inlineInputSet || inputFileSet || parsed.ReadStdin {
				return workflowArgs{}, fmt.Errorf("workflow input sources conflict: use one of inline text, --input <file>, or -")
			}
			parsed.InputFile = strings.TrimPrefix(arg, "--input=")
			if parsed.InputFile == "" {
				return workflowArgs{}, fmt.Errorf("workflow --input requires a file path")
			}
			inputFileSet = true
			continue
		}
		if options && strings.HasPrefix(arg, "-") && arg != "-" {
			return workflowArgs{}, fmt.Errorf("unknown workflow option %q", arg)
		}
		if inputFileSet || parsed.ReadStdin {
			return workflowArgs{}, fmt.Errorf("workflow input sources conflict: use one of inline text, --input <file>, or -")
		}
		if inlineInputSet {
			return workflowArgs{}, fmt.Errorf("unexpected workflow argument %q", arg)
		}
		if arg == "-" && options {
			parsed.ReadStdin = true
		} else {
			parsed.InlineInput = arg
			inlineInputSet = true
		}
	}

	return parsed, nil
}

func readWorkflowInputFile(path string) (input string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read workflow input file %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("read workflow input file %q: %w", path, closeErr)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("read workflow input file %q: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("workflow input file %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("workflow input file %q is not a regular file", path)
	}
	if info.Size() > maxWorkflowInputBytes {
		return "", fmt.Errorf("workflow input file %q exceeds the 1 MiB limit", path)
	}
	input, err = readWorkflowInput(file, fmt.Sprintf("input file %q", path))
	if err != nil {
		return "", err
	}
	return input, nil
}

func readWorkflowInput(reader io.Reader, source string) (string, error) {
	limited := io.LimitReader(reader, maxWorkflowInputBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read workflow %s: %w", source, err)
	}
	if int64(len(content)) > maxWorkflowInputBytes {
		return "", fmt.Errorf("workflow %s exceeds the 1 MiB limit", source)
	}
	return string(content), nil
}

func runWorkflow(ctx context.Context, script, workingDir, input string, agent workflow.AgentFunc, output io.Writer) error {
	err := workflow.RunFileWithAgentInputInWorkingDir(ctx, script, workingDir, input, output, agent)
	if err == nil {
		return nil
	}

	if doneErr, ok := errors.AsType[*workflow.DoneError](err); ok {
		if doneErr.Message != "" {
			_, _ = fmt.Fprintln(output, doneErr.Message)
		}
		return nil
	}

	return err
}
