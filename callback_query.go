package gorm

import (
	"errors"
	"fmt"
	"reflect"
	"context"

    "github.com/opentracing/opentracing-go"

)

// Define callbacks for querying
func init() {
	DefaultCallback.Query().Register("gorm:query", queryCallback)
	DefaultCallback.Query().Register("gorm:preload", preloadCallback)
	DefaultCallback.Query().Register("gorm:after_query", afterQueryCallback)
}

// queryCallback used to query data from database
func queryCallback(scope *Scope) {
	if _, skip := scope.InstanceGet("gorm:skip_query_callback"); skip {
		return
	}

	//we are only preloading relations, dont touch base model
	if _, skip := scope.InstanceGet("gorm:only_preload"); skip {
		return
	}

	defer scope.trace(NowFunc())
	val,_ := scope.Get("_context")

	rootCtx := context.Background()
    if ctx, ok := val.(context.Context); ok {
       rootCtx = ctx
    }

    span, childCtx := opentracing.StartSpanFromContext(
        rootCtx,
        "gorm:internal_queryCallback",
    )
    defer span.Finish()

	var (
		isSlice, isPtr bool
		resultType     reflect.Type
		results        = scope.IndirectValue()
	)

	if orderBy, ok := scope.Get("gorm:order_by_primary_key"); ok {
		if primaryField := scope.PrimaryField(); primaryField != nil {
			scope.Search.Order(fmt.Sprintf("%v.%v %v", scope.QuotedTableName(), scope.Quote(primaryField.DBName), orderBy))
		}
	}

	if value, ok := scope.Get("gorm:query_destination"); ok {
		results = indirect(reflect.ValueOf(value))
	}

	if kind := results.Kind(); kind == reflect.Slice {
		isSlice = true
		resultType = results.Type().Elem()
		results.Set(reflect.MakeSlice(results.Type(), 0, 0))

		if resultType.Kind() == reflect.Ptr {
			isPtr = true
			resultType = resultType.Elem()
		}
	} else if kind != reflect.Struct {
		scope.Err(errors.New("unsupported destination, should be slice or struct"))
		return
	}

	prepareSpan,_ := opentracing.StartSpanFromContext(
        childCtx,
        "gorm:internal_queryCallback:prepareQuery",
    )
	scope.prepareQuerySQL()
	prepareSpan.Finish()

	if !scope.HasError() {
		scope.db.RowsAffected = 0

		if str, ok := scope.Get("gorm:query_hint"); ok {
			scope.SQL = fmt.Sprint(str) + scope.SQL
		}

		querySpan,queryCtx := opentracing.StartSpanFromContext(
            childCtx,
            "gorm:internal_queryCallback:query",
        )

		rows, err := scope.SQLDB().Query(scope.SQL, scope.SQLVars...)
        defer  querySpan.Finish()

		if scope.Err(err) == nil {
			defer rows.Close()
            scanSpan,_ := opentracing.StartSpanFromContext(
                queryCtx,
                "gorm:internal_queryCallback:scan",
            )
			columns, _ := rows.Columns()
			for rows.Next() {
				scope.db.RowsAffected++

				elem := results
				if isSlice {
					elem = reflect.New(resultType).Elem()
				}

				scope.scan(rows, columns, scope.New(elem.Addr().Interface()).Fields())

				if isSlice {
					if isPtr {
						results.Set(reflect.Append(results, elem.Addr()))
					} else {
						results.Set(reflect.Append(results, elem))
					}
				}
			}


			if err := rows.Err(); err != nil {
			    scanSpan.SetTag("rows.err", err.Error())
				scope.Err(err)
			} else if scope.db.RowsAffected == 0 && !isSlice {
                scanSpan.SetTag("rows.err",ErrRecordNotFound.Error())
				scope.Err(ErrRecordNotFound)
			}
            scanSpan.Finish()
		}

	}

}

// afterQueryCallback will invoke `AfterFind` method after querying
func afterQueryCallback(scope *Scope) {
	if !scope.HasError() {
		scope.CallMethod("AfterFind")
	}
}
