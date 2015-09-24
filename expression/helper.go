// Copyright 2013 The ql Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSES/QL-LICENSE file.

// Copyright 2015 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

// Copyright 2014 The TiDB Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found PatternIn the LICENSE file.

package expression

import (
	"strconv"
	"strings"
	"time"

	"github.com/juju/errors"
	"github.com/ngaut/log"
	"github.com/pingcap/tidb/context"

	"github.com/pingcap/tidb/expression/builtin"
	"github.com/pingcap/tidb/model"
	mysql "github.com/pingcap/tidb/mysqldef"
	"github.com/pingcap/tidb/parser/opcode"
	"github.com/pingcap/tidb/sessionctx/variable"
	"github.com/pingcap/tidb/util/types"
)

const (
	// ExprEvalDefaultName is the key saving default column name for Default expression.
	ExprEvalDefaultName = "$defaultName"
	// ExprEvalIdentFunc is the key saving a function to retrieve value for identifier name.
	ExprEvalIdentFunc = "$identFunc"
	// ExprEvalPositionFunc is the key saving a Position expresion.
	ExprEvalPositionFunc = "$positionFunc"
	// ExprEvalValuesFunc is the key saving a function to retrieve value for column name.
	ExprEvalValuesFunc = "$valuesFunc"
)

var (
	// CurrentTimestamp is the keyword getting default value for datetime and timestamp type.
	CurrentTimestamp = "CURRENT_TIMESTAMP"
	// CurrentTimeExpr is the expression retireving default value for datetime and timestamp type.
	CurrentTimeExpr = &Ident{model.NewCIStr(CurrentTimestamp)}
	// ZeroTimestamp shows the zero datetime and timestamp.
	ZeroTimestamp = "0000-00-00 00:00:00"
)

var (
	errDefaultValue = errors.New("invalid default value")
)

// TypeStar is the type for *
type TypeStar string

// Expr removes parenthese expression, e.g, (expr) -> expr.
func Expr(v interface{}) Expression {
	e := v.(Expression)
	for {
		x, ok := e.(*PExpr)
		if !ok {
			return e
		}
		e = x.Expr
	}
}

func cloneExpressionList(list []Expression) []Expression {
	r := make([]Expression, len(list))
	for i, v := range list {
		r[i] = v.Clone()
	}
	return r
}

// FastEval evaluates Value and static +/- Unary expression and returns its value.
func FastEval(v interface{}) interface{} {
	switch x := v.(type) {
	case Value:
		return x.Val
	case int64, uint64:
		return v
	case *UnaryOperation:
		if x.Op != opcode.Plus && x.Op != opcode.Minus {
			return nil
		}
		if !x.IsStatic() {
			return nil
		}
		m := map[interface{}]interface{}{}
		return Eval(x, nil, m)
	default:
		return nil
	}
}

// IsQualified returns whether name contains ".".
func IsQualified(name string) bool {
	return strings.Contains(name, ".")
}

// Eval is a helper function evaluates expression v and do a panic if evaluating error.
func Eval(v Expression, ctx context.Context, env map[interface{}]interface{}) (y interface{}) {
	var err error
	y, err = v.Eval(ctx, env)
	if err != nil {
		panic(err) // panic ok here
	}
	return
}

// MentionedAggregateFuncs returns a list of the Call expression which is aggregate function.
func MentionedAggregateFuncs(e Expression) []Expression {
	var m []Expression
	mentionedAggregateFuncs(e, &m)
	return m
}

func mentionedAggregateFuncs(e Expression, m *[]Expression) {
	switch x := e.(type) {
	case Value, *Value, *Variable, *Default,
		*Ident, SubQuery, *Position, *ExistsSubQuery:
		// nop
	case *BinaryOperation:
		mentionedAggregateFuncs(x.L, m)
		mentionedAggregateFuncs(x.R, m)
	case *Call:
		f, ok := builtin.Funcs[strings.ToLower(x.F)]
		if !ok {
			log.Errorf("unknown function %s", x.F)
			return
		}

		if f.IsAggregate {
			// if f is aggregate function, we don't need check the arguments,
			// because using an aggregate function in the aggregate arg like count(max(c1)) is invalid
			// TODO: check whether argument contains an aggregate function and return error.
			*m = append(*m, e)
			return
		}

		for _, e := range x.Args {
			mentionedAggregateFuncs(e, m)
		}
	case *IsNull:
		mentionedAggregateFuncs(x.Expr, m)
	case *PExpr:
		mentionedAggregateFuncs(x.Expr, m)
	case *PatternIn:
		mentionedAggregateFuncs(x.Expr, m)
		for _, e := range x.List {
			mentionedAggregateFuncs(e, m)
		}
	case *PatternLike:
		mentionedAggregateFuncs(x.Expr, m)
		mentionedAggregateFuncs(x.Pattern, m)
	case *UnaryOperation:
		mentionedAggregateFuncs(x.V, m)
	case *ParamMarker:
		if x.Expr != nil {
			mentionedAggregateFuncs(x.Expr, m)
		}
	case *FunctionCast:
		if x.Expr != nil {
			mentionedAggregateFuncs(x.Expr, m)
		}
	case *FunctionConvert:
		if x.Expr != nil {
			mentionedAggregateFuncs(x.Expr, m)
		}
	case *FunctionSubstring:
		if x.StrExpr != nil {
			mentionedAggregateFuncs(x.StrExpr, m)
		}
		if x.Pos != nil {
			mentionedAggregateFuncs(x.Pos, m)
		}
		if x.Len != nil {
			mentionedAggregateFuncs(x.Len, m)
		}
	case *FunctionCase:
		if x.Value != nil {
			mentionedAggregateFuncs(x.Value, m)
		}
		for _, w := range x.WhenClauses {
			mentionedAggregateFuncs(w, m)
		}
		if x.ElseClause != nil {
			mentionedAggregateFuncs(x.ElseClause, m)
		}
	case *WhenClause:
		mentionedAggregateFuncs(x.Expr, m)
		mentionedAggregateFuncs(x.Result, m)
	case *IsTruth:
		mentionedAggregateFuncs(x.Expr, m)
	case *Between:
		mentionedAggregateFuncs(x.Expr, m)
		mentionedAggregateFuncs(x.Left, m)
		mentionedAggregateFuncs(x.Right, m)
	case *Row:
		for _, expr := range x.Values {
			mentionedAggregateFuncs(expr, m)
		}
	case *CompareSubQuery:
		mentionedAggregateFuncs(x.L, m)
	default:
		log.Errorf("Unknown Expression: %T", e)
	}
}

// ContainAggregateFunc checks whether expression e contains an aggregate function, like count(*) or other.
func ContainAggregateFunc(e Expression) bool {
	m := MentionedAggregateFuncs(e)
	return len(m) > 0
}

// MentionedColumns returns a list of names for Ident expression.
func MentionedColumns(e Expression) []string {
	var names []string
	mcv := &MentionedColumnsVisitor{
		Columns:map[string]struct{}{},
	}
	e.Accept(mcv)
	for k := range mcv.Columns {
		names = append(names, k)
	}
	return names
}

func staticExpr(e Expression) (Expression, error) {
	if e.IsStatic() {
		v, err := e.Eval(nil, nil)
		if err != nil {
			return nil, err
		}

		if v == nil {
			return Value{nil}, nil
		}

		return Value{v}, nil
	}

	return e, nil
}

// IsCurrentTimeExpr returns whether e is CurrentTimeExpr.
func IsCurrentTimeExpr(e Expression) bool {
	x, ok := e.(*Ident)
	if !ok {
		return false
	}

	return x.Equal(CurrentTimeExpr)
}

func getSystemTimestamp(ctx context.Context) (time.Time, error) {
	value := time.Now()

	if ctx == nil {
		return value, nil
	}

	// check whether use timestamp varibale
	sessionVars := variable.GetSessionVars(ctx)
	if v, ok := sessionVars.Systems["timestamp"]; ok {
		if v != "" {
			timestamp, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return time.Time{}, errors.Trace(err)
			}

			if timestamp <= 0 {
				return value, nil
			}

			return time.Unix(timestamp, 0), nil
		}
	}

	return value, nil
}

// GetTimeValue gets the time value with type tp.
func GetTimeValue(ctx context.Context, v interface{}, tp byte, fsp int) (interface{}, error) {
	return getTimeValue(ctx, v, tp, fsp)
}

func getTimeValue(ctx context.Context, v interface{}, tp byte, fsp int) (interface{}, error) {
	value := mysql.Time{
		Type: tp,
		Fsp:  fsp,
	}

	defaultTime, err := getSystemTimestamp(ctx)
	if err != nil {
		return nil, errors.Trace(err)
	}

	switch x := v.(type) {
	case string:
		if x == CurrentTimestamp {
			value.Time = defaultTime
		} else if x == ZeroTimestamp {
			value, _ = mysql.ParseTimeFromNum(0, tp, fsp)
		} else {
			value, err = mysql.ParseTime(x, tp, fsp)
			if err != nil {
				return nil, errors.Trace(err)
			}
		}
	case Value:
		switch xval := x.Val.(type) {
		case string:
			value, err = mysql.ParseTime(xval, tp, fsp)
			if err != nil {
				return nil, errors.Trace(err)
			}
		case int64:
			value, err = mysql.ParseTimeFromNum(int64(xval), tp, fsp)
			if err != nil {
				return nil, errors.Trace(err)
			}
		case nil:
			return nil, nil
		default:
			return nil, errors.Trace(errDefaultValue)
		}
	case *Ident:
		if x.Equal(CurrentTimeExpr) {
			return CurrentTimestamp, nil
		}

		return nil, errors.Trace(errDefaultValue)
	case *UnaryOperation:
		// support some expression, like `-1`
		m := map[interface{}]interface{}{}
		v := Eval(x, nil, m)
		ft := types.NewFieldType(mysql.TypeLonglong)
		xval, err := types.Convert(v, ft)
		if err != nil {
			return nil, errors.Trace(err)
		}

		value, err = mysql.ParseTimeFromNum(xval.(int64), tp, fsp)
		if err != nil {
			return nil, errors.Trace(err)
		}
	default:
		return nil, nil
	}

	return value, nil
}

// EvalBoolExpr evaluates an expression and convert its return value to bool.
func EvalBoolExpr(ctx context.Context, expr Expression, m map[interface{}]interface{}) (bool, error) {
	val, err := expr.Eval(ctx, m)
	if err != nil {
		return false, err
	}
	if val == nil {
		return false, nil
	}

	x, err := types.ToBool(val)
	if err != nil {
		return false, err
	}

	return x != 0, nil
}

// CheckOneColumn checks whether expression e has only one column for the evaluation result.
// Now most of the expressions have one column except Row expression.
func CheckOneColumn(ctx context.Context, e Expression) error {
	n, err := columnCount(ctx, e)
	if err != nil {
		return errors.Trace(err)
	}

	if n != 1 {
		return errors.Errorf("Operand should contain 1 column(s)")
	}

	return nil
}

// CheckAllOneColumns checks all expressions have one column.
func CheckAllOneColumns(ctx context.Context, args ...Expression) error {
	for _, e := range args {
		if err := CheckOneColumn(ctx, e); err != nil {
			return err
		}
	}

	return nil
}

func columnCount(ctx context.Context, e Expression) (int, error) {
	switch x := e.(type) {
	case *Row:
		n := len(x.Values)
		if n <= 1 {
			return 0, errors.Errorf("Operand should contain >= 2 columns for Row")
		}
		return n, nil
	case SubQuery:
		return x.ColumnCount(ctx)
	default:
		return 1, nil
	}
}

func hasSameColumnCount(ctx context.Context, e Expression, args ...Expression) error {
	l, err := columnCount(ctx, e)
	if err != nil {
		return errors.Trace(err)
	}
	var n int
	for _, arg := range args {
		n, err = columnCount(ctx, arg)
		if err != nil {
			return errors.Trace(err)
		}

		if n != l {
			return errors.Errorf("Operand should contain %d column(s)", l)
		}
	}

	return nil
}
