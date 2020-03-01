package eksconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

const (
	// EnvironmentVariablePrefix is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefix = "AWS_K8S_TESTER_EKS_"
	// EnvironmentVariablePrefixParameters is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixParameters = "AWS_K8S_TESTER_EKS_PARAMETERS_"
	// EnvironmentVariablePrefixAddOnManagedNodeGroups is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnManagedNodeGroups = "AWS_K8S_TESTER_EKS_ADD_ON_MANAGED_NODE_GROUPS_"
	// EnvironmentVariablePrefixAddOnNLBHelloWorld is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnNLBHelloWorld = "AWS_K8S_TESTER_EKS_ADD_ON_NLB_HELLO_WORLD_"
	// EnvironmentVariablePrefixAddOnALB2048 is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnALB2048 = "AWS_K8S_TESTER_EKS_ADD_ON_ALB_2048_"
	// EnvironmentVariablePrefixAddOnJobPerl is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnJobPerl = "AWS_K8S_TESTER_EKS_ADD_ON_JOB_PERL_"
	// EnvironmentVariablePrefixAddOnJobEcho is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnJobEcho = "AWS_K8S_TESTER_EKS_ADD_ON_JOB_ECHO_"
	// EnvironmentVariablePrefixAddOnSecrets is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnSecrets = "AWS_K8S_TESTER_EKS_ADD_ON_SECRETS_"
	// EnvironmentVariablePrefixAddOnIRSA is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnIRSA = "AWS_K8S_TESTER_EKS_ADD_ON_IRSA_"
	// EnvironmentVariablePrefixAddOnFargate is the environment variable prefix used for "eksconfig".
	EnvironmentVariablePrefixAddOnFargate = "AWS_K8S_TESTER_EKS_ADD_ON_FARGATE_"
)

// UpdateFromEnvs updates fields from environmental variables.
// Empty values are ignored and do not overwrite fields with empty values.
// WARNING: The environmetal variable value always overwrites current field
// values if there's a conflict.
func (cfg *Config) UpdateFromEnvs() (err error) {
	cfg.mu.Lock()
	defer func() {
		cfg.unsafeSync()
		cfg.mu.Unlock()
	}()

	var vv interface{}
	vv, err = parseEnvs(EnvironmentVariablePrefix, cfg)
	if err != nil {
		return err
	}
	if av, ok := vv.(*Config); ok {
		cfg = av
	} else {
		return fmt.Errorf("expected *Config, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixParameters, cfg.Parameters)
	if err != nil {
		return err
	}
	if av, ok := vv.(*Parameters); ok {
		cfg.Parameters = av
	} else {
		return fmt.Errorf("expected *Parameters, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnManagedNodeGroups, cfg.AddOnManagedNodeGroups)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnManagedNodeGroups); ok {
		cfg.AddOnManagedNodeGroups = av
	} else {
		return fmt.Errorf("expected *AddOnManagedNodeGroups, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnNLBHelloWorld, cfg.AddOnNLBHelloWorld)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnNLBHelloWorld); ok {
		cfg.AddOnNLBHelloWorld = av
	} else {
		return fmt.Errorf("expected *AddOnNLBHelloWorld, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnALB2048, cfg.AddOnALB2048)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnALB2048); ok {
		cfg.AddOnALB2048 = av
	} else {
		return fmt.Errorf("expected *AddOnALB2048, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnJobPerl, cfg.AddOnJobPerl)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnJobPerl); ok {
		cfg.AddOnJobPerl = av
	} else {
		return fmt.Errorf("expected *AddOnJobPerl, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnJobEcho, cfg.AddOnJobEcho)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnJobEcho); ok {
		cfg.AddOnJobEcho = av
	} else {
		return fmt.Errorf("expected *AddOnJobEcho, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnSecrets, cfg.AddOnSecrets)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnSecrets); ok {
		cfg.AddOnSecrets = av
	} else {
		return fmt.Errorf("expected *AddOnSecrets, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnIRSA, cfg.AddOnIRSA)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnIRSA); ok {
		cfg.AddOnIRSA = av
	} else {
		return fmt.Errorf("expected *AddOnIRSA, got %T", vv)
	}

	vv, err = parseEnvs(EnvironmentVariablePrefixAddOnFargate, cfg.AddOnFargate)
	if err != nil {
		return err
	}
	if av, ok := vv.(*AddOnFargate); ok {
		cfg.AddOnFargate = av
	} else {
		return fmt.Errorf("expected *AddOnFargate, got %T", vv)
	}

	return nil
}

func parseEnvs(pfx string, addOn interface{}) (interface{}, error) {
	tp, vv := reflect.TypeOf(addOn).Elem(), reflect.ValueOf(addOn).Elem()
	for i := 0; i < tp.NumField(); i++ {
		jv := tp.Field(i).Tag.Get("json")
		if jv == "" {
			continue
		}
		jv = strings.Replace(jv, ",omitempty", "", -1)
		jv = strings.ToUpper(strings.Replace(jv, "-", "_", -1))
		env := pfx + jv
		sv := os.Getenv(env)
		if sv == "" {
			continue
		}
		if tp.Field(i).Tag.Get("read-only") == "true" { // skip updating read-only field
			continue
		}
		fieldName := tp.Field(i).Name

		switch vv.Field(i).Type().Kind() {
		case reflect.String:
			vv.Field(i).SetString(sv)

		case reflect.Bool:
			bb, err := strconv.ParseBool(sv)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetBool(bb)

		case reflect.Int, reflect.Int32, reflect.Int64:
			iv, err := strconv.ParseInt(sv, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetInt(iv)

		case reflect.Uint, reflect.Uint32, reflect.Uint64:
			iv, err := strconv.ParseUint(sv, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetUint(iv)

		case reflect.Float32, reflect.Float64:
			fv, err := strconv.ParseFloat(sv, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetFloat(fv)

		case reflect.Slice: // only supports "[]string" for now
			ss := strings.Split(sv, ",")
			if len(ss) < 1 {
				continue
			}
			slice := reflect.MakeSlice(reflect.TypeOf([]string{}), len(ss), len(ss))
			for j := range ss {
				slice.Index(j).SetString(ss[j])
			}
			vv.Field(i).Set(slice)

		case reflect.Map:
			switch fieldName {
			case "Tags":
				vv.Field(i).Set(reflect.ValueOf(make(map[string]string)))
				for _, pair := range strings.Split(sv, ";") {
					fields := strings.Split(pair, "=")
					if len(fields) != 2 {
						return nil, fmt.Errorf("map %q for %q has unexpected format (e.g. should be 'a=b;c;d,e=f')", sv, fieldName)
					}
					vv.Field(i).SetMapIndex(reflect.ValueOf(fields[0]), reflect.ValueOf(fields[1]))
				}

			case "MNGs":
				mng := make(map[string]MNG)
				if err := json.Unmarshal([]byte(sv), &mng); err != nil {
					return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
				}
				vv.Field(i).Set(reflect.ValueOf(mng))

			default:
				return nil, fmt.Errorf("field %q not supported for reflect.Map", fieldName)
			}

		default:
			return nil, fmt.Errorf("%q (type %v) is not supported as an env", env, vv.Field(i).Type())
		}
	}
	return addOn, nil
}
