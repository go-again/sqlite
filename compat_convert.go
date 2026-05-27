package sqlite

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"time"
)

var (
	errType     = reflect.TypeFor[error]()
	timeType    = reflect.TypeFor[time.Time]()
	anyType     = reflect.TypeFor[any]()
	driverValTy = reflect.TypeFor[driver.Value]()
)

// valueConverter returns a function that coerces a SQLite-side driver.Value
// (always one of: nil, int64, float64, string, []byte) into the requested Go
// type. Returning an error means no conversion is available; the caller should
// short-circuit and surface a function-registration error.
func valueConverter(dst reflect.Type) (func(driver.Value) (reflect.Value, error), error) {
	switch dst.Kind() {
	case reflect.Bool:
		return func(v driver.Value) (reflect.Value, error) {
			i, err := toInt64(v)
			if err != nil {
				return reflect.Value{}, err
			}
			return reflect.ValueOf(i != 0), nil
		}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(v driver.Value) (reflect.Value, error) {
			i, err := toInt64(v)
			if err != nil {
				return reflect.Value{}, err
			}
			return reflect.ValueOf(i).Convert(dst), nil
		}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return func(v driver.Value) (reflect.Value, error) {
			i, err := toInt64(v)
			if err != nil {
				return reflect.Value{}, err
			}
			return reflect.ValueOf(uint64(i)).Convert(dst), nil
		}, nil
	case reflect.Float32, reflect.Float64:
		return func(v driver.Value) (reflect.Value, error) {
			f, err := toFloat64(v)
			if err != nil {
				return reflect.Value{}, err
			}
			return reflect.ValueOf(f).Convert(dst), nil
		}, nil
	case reflect.String:
		return func(v driver.Value) (reflect.Value, error) {
			s, err := toString(v)
			if err != nil {
				return reflect.Value{}, err
			}
			return reflect.ValueOf(s).Convert(dst), nil
		}, nil
	case reflect.Slice:
		if dst.Elem().Kind() == reflect.Uint8 {
			// []byte
			return func(v driver.Value) (reflect.Value, error) {
				if v == nil {
					return reflect.Zero(dst), nil
				}
				switch t := v.(type) {
				case []byte:
					return reflect.ValueOf(t).Convert(dst), nil
				case string:
					return reflect.ValueOf([]byte(t)).Convert(dst), nil
				default:
					return reflect.Value{}, fmt.Errorf("cannot convert %T to []byte", v)
				}
			}, nil
		}
	case reflect.Interface:
		if dst == anyType || dst == driverValTy {
			return func(v driver.Value) (reflect.Value, error) {
				if v == nil {
					return reflect.Zero(dst), nil
				}
				return reflect.ValueOf(v), nil
			}, nil
		}
	case reflect.Struct:
		if dst == timeType {
			return func(v driver.Value) (reflect.Value, error) {
				switch t := v.(type) {
				case int64:
					return reflect.ValueOf(time.Unix(t, 0).UTC()), nil
				case string:
					tt, err := parseTime(t)
					if err != nil {
						return reflect.Value{}, err
					}
					return reflect.ValueOf(tt), nil
				default:
					return reflect.Value{}, fmt.Errorf("cannot convert %T to time.Time", v)
				}
			}, nil
		}
	}
	return nil, fmt.Errorf("unsupported argument type %v", dst)
}

// goToDriverValue converts an arbitrary Go value (the result of a reflective
// user function call) into one of the driver.Value kinds SQLite understands.
func goToDriverValue(v reflect.Value) driver.Value {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return int64(1)
		}
		return int64(0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(v.Uint())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.IsNil() {
				return nil
			}
			return v.Bytes()
		}
	case reflect.Interface, reflect.Ptr: //nolint:govet // inline: reflect.Ptr alias kept for readability over the numeric constant
		if v.IsNil() {
			return nil
		}
		return goToDriverValue(v.Elem())
	case reflect.Struct:
		if v.Type() == timeType {
			return v.Interface().(time.Time).Unix()
		}
	}
	return v.Interface()
}

func toInt64(v driver.Value) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case float64:
		return int64(t), nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case nil:
		return 0, nil
	case string:
		return 0, fmt.Errorf("cannot convert string to integer")
	case []byte:
		return 0, fmt.Errorf("cannot convert bytes to integer")
	}
	return 0, fmt.Errorf("cannot convert %T to integer", v)
}

func toFloat64(v driver.Value) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int64:
		return float64(t), nil
	case nil:
		return 0, nil
	}
	return 0, fmt.Errorf("cannot convert %T to float64", v)
}

func toString(v driver.Value) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case []byte:
		return string(t), nil
	case int64:
		return fmt.Sprintf("%d", t), nil
	case float64:
		return fmt.Sprintf("%g", t), nil
	case nil:
		return "", nil
	}
	return "", fmt.Errorf("cannot convert %T to string", v)
}

func parseTime(s string) (time.Time, error) {
	for _, f := range parseTimeFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as time", s)
}
