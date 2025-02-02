package util

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/jwalton/gchalk"
	"golang.org/x/mod/modfile"
)

func AddOmitemptyToJSONTags(obj interface{}) interface{} {
	originalType := reflect.TypeOf(obj)
	originalValue := reflect.ValueOf(obj)

	newFields := make([]reflect.StructField, 0, originalType.NumField())
	for i := 0; i < originalType.NumField(); i++ {
		field := originalType.Field(i)
		tag := field.Tag.Get("json")
		if tag != "" && !reflect.DeepEqual(tag, "-") {
			tag += ",omitempty"
		}
		newField := reflect.StructField{
			Name: field.Name,
			Type: field.Type,
			Tag:  reflect.StructTag(fmt.Sprintf(`json:"%s"`, tag)),
		}
		newFields = append(newFields, newField)
	}

	newStructType := reflect.StructOf(newFields)
	newStruct := reflect.New(newStructType).Elem()

	for i := 0; i < originalType.NumField(); i++ {
		newStruct.Field(i).Set(originalValue.Field(i))
	}

	return newStruct.Interface()
}

// StrPad returns the input string padded on the left, 
//right or both sides using padType to the specified padding length padLength.
// Example:
// input := "Codes";
// StrPad(input, 10, " ", "RIGHT")        // produces "Codes     "
// StrPad(input, 10, "-=", "LEFT")        // produces "=-=-=Codes"
// StrPad(input, 10, "_", "BOTH")         // produces "__Codes___"
// StrPad(input, 6, "___", "RIGHT")       // produces "Codes_"
// StrPad(input, 3, "*", "RIGHT")         // produces "Codes"
// taken from // https://gist.github.com/asessa/3aaec43d93044fc42b7c6d5f728cb039
func StrPad(input string, padLength int, padString, padType string) string {
	if len(input) >= padLength {
		return input
	}

	repeat := strings.Repeat(padString, (padLength-len(input))/len(padString)+1)

	switch padType {
	case "RIGHT":
		return (input + repeat)[:padLength]
	case "LEFT":
		return repeat[:padLength-len(input)] + input
	case "BOTH":
		totalPadding := padLength - len(input)
		leftPadding := totalPadding / 2
		rightPadding := totalPadding - leftPadding
		return repeat[:leftPadding] + input + repeat[:rightPadding]
	default:
		return input
	}
}

func GetHandler(projectName string, handler http.Handler) (funcName string) {
	// https://github.com/go-chi/chi/issues/424
	funcName = runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name()
	base := filepath.Base(funcName)

	nameSplit := strings.Split(funcName, "")
	names := nameSplit[len(projectName):]
	path := strings.Join(names, "")

	pathSplit := strings.Split(path, "/")
	path = strings.Join(pathSplit[:len(pathSplit)-1], "/")

	sFull := strings.Split(base, ".")
	s := sFull[len(sFull)-1:]

	s = strings.Split(s[0], "")
	if len(s) <= 4 && len(sFull) >= 3 {
		s = sFull[len(sFull)-3 : len(sFull)-2]
		return "@" + gchalk.Blue(strings.Join(s, ""))
	}
	s = s[:len(s)-3]
	funcName = strings.Join(s, "")

	return path + "@" + gchalk.Blue(funcName)
}

// adapted from https://stackoverflow.com/a/63393712/1033134// GetModName retrieves the module name from `go.mod`.
func GetModName() string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		log.Fatalf("Failed to read go.mod: %v", err)
	}
	modPath := modfile.ModulePath(data)
	if modPath == "" {
		log.Fatalf("Module path not found in go.mod")
	}
	return modPath
}
