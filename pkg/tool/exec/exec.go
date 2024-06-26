// Copyright 2019 CUE Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package exec

import (
	"fmt"
	"os/exec"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/errors"
	"cuelang.org/go/internal/task"
)

func init() {
	task.Register("tool/exec.Run", newExecCmd)

	// For backwards compatibility.
	task.Register("exec", newExecCmd)
}

type execCmd struct{}

func newExecCmd(v cue.Value) (task.Runner, error) {
	return &execCmd{}, nil
}

func (c *execCmd) Run(ctx *task.Context) (res interface{}, err error) {
	cmd, doc, err := mkCommand(ctx)
	if err != nil {
		return cue.Value{}, err
	}

	// TODO: set environment variables, if defined.
	stream := func(name string) (stream cue.Value, ok bool) {
		c := ctx.Obj.Lookup(name)
		// Although the schema defines a default versions, older implementations
		// may not use it yet.
		if !c.Exists() {
			return
		}
		if err := c.Null(); ctx.Err != nil || err == nil {
			return
		}
		return c, true
	}

	if v, ok := stream("stdin"); !ok {
		cmd.Stdin = ctx.Stdin
	} else if cmd.Stdin, err = v.Reader(); err != nil {
		return nil, errors.Wrapf(err, v.Pos(), "invalid input")
	}
	_, captureOut := stream("stdout")
	if !captureOut {
		cmd.Stdout = ctx.Stdout
	}
	_, captureErr := stream("stderr")
	if !captureErr {
		cmd.Stderr = ctx.Stderr
	}

	// TODO(mvdan): exec.Run declares mustSucceed as a regular field with a default of true.
	// We should be able to rely on that here, removing the need for Exists and repeating the default.
	mustSucceed := true
	if v := ctx.Obj.LookupPath(cue.ParsePath("mustSucceed")); v.Exists() {
		mustSucceed, err = v.Bool()
		if err != nil {
			return nil, errors.Wrapf(err, v.Pos(), "invalid bool value")
		}
	}

	update := map[string]interface{}{}
	if captureOut {
		var stdout []byte
		stdout, err = cmd.Output()
		update["stdout"] = string(stdout)
	} else {
		err = cmd.Run()
	}
	update["success"] = err == nil

	if err == nil {
		return update, nil
	}

	if captureErr {
		if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
			update["stderr"] = string(exit.Stderr)
		} else {
			update["stderr"] = err.Error()
		}
	}
	if !mustSucceed {
		return update, nil
	}

	return nil, fmt.Errorf("command %q failed: %v", doc, err)
}

func mkCommand(ctx *task.Context) (c *exec.Cmd, doc string, err error) {
	var bin string
	var args []string

	v := ctx.Lookup("cmd")
	if ctx.Err != nil {
		return nil, "", ctx.Err
	}

	switch v.Kind() {
	case cue.StringKind:
		str := ctx.String("cmd")
		doc = str
		list := strings.Fields(str)
		bin = list[0]
		args = append(args, list[1:]...)

	case cue.ListKind:
		list, _ := v.List()
		if !list.Next() {
			return nil, "", errors.New("empty command list")
		}
		bin, err = list.Value().String()
		if err != nil {
			return nil, "", err
		}
		doc += bin
		for list.Next() {
			str, err := list.Value().String()
			if err != nil {
				return nil, "", err
			}
			args = append(args, str)
			doc += " " + str
		}
	}

	if bin == "" {
		return nil, "", errors.New("empty command")
	}

	cmd := exec.CommandContext(ctx.Context, bin, args...)

	cmd.Dir, _ = ctx.Obj.LookupPath(cue.ParsePath("dir")).String()

	env := ctx.Obj.LookupPath(cue.ParsePath("env"))

	// List case.
	for iter, _ := env.List(); iter.Next(); {
		v, _ := iter.Value().Default()
		str, err := v.String()
		if err != nil {
			return nil, "", errors.Wrapf(err, v.Pos(),
				"invalid environment variable value %q", v)
		}
		cmd.Env = append(cmd.Env, str)
	}

	// Struct case.
	for iter, _ := env.Fields(); iter.Next(); {
		label := iter.Label()
		v, _ := iter.Value().Default()
		var str string
		switch v.Kind() {
		case cue.StringKind:
			str, _ = v.String()
		case cue.IntKind, cue.FloatKind, cue.NumberKind:
			str = fmt.Sprint(v)
		default:
			return nil, "", errors.Newf(v.Pos(),
				"invalid environment variable value %q", v)
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", label, str))
	}

	return cmd, doc, nil
}
