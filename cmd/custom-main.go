/*
 * MinIO Client, (C) 2018 MinIO, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cmd

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/minio/cli"
	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio/pkg/mimedb"
)

var (
	customFlags = []cli.Flag{
		cli.StringFlag{
			Name:  "query, e",
			Usage: "custom query expression",
			Value: "select * from s3object",
		},
		/*
			cli.BoolFlag{
				Name:  "recursive, r",
				Usage: "custom query recursively",
			},
			cli.StringFlag{
				Name:  "csv-input",
				Usage: "csv input serialization option",
			},
			cli.StringFlag{
				Name:  "json-input",
				Usage: "json input serialization option",
			},
			cli.StringFlag{
				Name:  "compression",
				Usage: "input compression type",
			},
			cli.StringFlag{
				Name:  "csv-output",
				Usage: "csv output serialization option",
			},
			cli.StringFlag{
				Name:  "csv-output-header",
				Usage: "optional csv output header ",
			},
			cli.StringFlag{
				Name:  "json-output",
				Usage: "json output serialization option",
			},
		*/
	}
)

// Display contents of a file.
var customCmd = cli.Command{
	Name:   "custom",
	Usage:  "run custom queries on objects",
	Action: mainCustom,
	Before: setGlobalsFromContext,
	Flags:  append(append(customFlags, ioFlags...), globalFlags...),
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] TARGET [TARGET...]
{{if .VisibleFlags}}	       
FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}{{end}}

EXAMPLES:
  1. Some sort of our custom command example
`,
}

// valid CSV and JSON keys for input/output serialization
var (
	validCSVCommonKeysCustom = []string{"FieldDelimiter", "QuoteChar", "QuoteEscChar"}
	validCSVInputKeysCustom  = []string{"Comments", "FileHeader", "QuotedRecordDelimiter", "RecordDelimiter"}
	validCSVOutputKeysCustom = []string{"QuoteFields"}

	validJSONInputKeysCustom           = []string{"Type"}
	validJSONCSVCommonOutputKeysCustom = []string{"RecordDelimiter"}

	// mapping of abbreviation to long form name of CSV and JSON input/output serialization keys
	validCSVInputAbbrKeysCustom   = map[string]string{"cc": "Comments", "fh": "FileHeader", "qrd": "QuotedRecordDelimiter", "rd": "RecordDelimiter", "fd": "FieldDelimiter", "qc": "QuoteChar", "qec": "QuoteEscChar"}
	validCSVOutputAbbrKeysCustom  = map[string]string{"qf": "QuoteFields", "rd": "RecordDelimiter", "fd": "FieldDelimiter", "qc": "QuoteChar", "qec": "QuoteEscChar"}
	validJSONOutputAbbrKeysCustom = map[string]string{"rd": "RecordDelimiter"}
)

// parseKVArgsCustom parses string of the form k=v delimited by ","
// into a map of k-v pairs
func parseKVArgsCustom(is string) (map[string]string, *probe.Error) {
	kvmap := make(map[string]string)
	var key, value string
	var s, e int  // tracking start and end of value
	var index int // current index in string
	if is != "" {
		for index < len(is) {
			i := strings.Index(is[index:], "=")
			if i == -1 {
				return nil, probe.NewError(errors.New("Arguments should be of the form key=value,... "))
			}
			key = is[index : index+i]
			s = i + index + 1
			e = strings.Index(is[s:], ",")
			delimFound := false
			for !delimFound {
				if e == -1 || e+s >= len(is) {
					delimFound = true
					break
				}
				if string(is[s+e]) != "," {
					delimFound = true
					if string(is[s+e-1]) == "," {
						e--
					}
				} else {
					e++
				}
			}
			var vEnd = len(is)
			if e != -1 {
				vEnd = s + e
			}

			value = is[s:vEnd]
			index = vEnd + 1
			if _, ok := kvmap[strings.ToLower(key)]; ok {
				return nil, probe.NewError(fmt.Errorf("More than one key=value found for %s", strings.TrimSpace(key)))
			}
			kvmap[strings.ToLower(key)] = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r").Replace(value)
		}
	}
	return kvmap, nil
}

// returns a string with list of serialization options and abbreviation(s) if any
func fmtStringCustom(validAbbr map[string]string, validKeys []string) string {
	var sb strings.Builder
	i := 0
	for k, v := range validAbbr {
		sb.WriteString(fmt.Sprintf("%s(%s) ", v, k))
		i++
		if i != len(validAbbr) {
			sb.WriteString(",")
		}
	}
	if len(sb.String()) == 0 {
		for _, k := range validKeys {
			sb.WriteString(fmt.Sprintf("%s ", k))
		}
	}
	return sb.String()
}

// parses the input string and constructs a k-v map, replacing any abbreviated keys with actual keys
func parseSerializationOptsCustom(inp string, validKeys []string, validAbbrKeys map[string]string) (map[string]string, *probe.Error) {
	if validAbbrKeys == nil {
		validAbbrKeys = make(map[string]string)
	}
	validKeyFn := func(key string, validKeys []string) bool {
		for _, name := range validKeys {
			if strings.EqualFold(name, key) {
				return true
			}
		}
		return false
	}
	kv, err := parseKVArgsCustom(inp)
	if err != nil {
		return nil, err
	}
	ikv := make(map[string]string)
	for k, v := range kv {
		fldName, ok := validAbbrKeys[strings.ToLower(k)]
		if ok {
			ikv[strings.ToLower(fldName)] = v
		} else {
			ikv[strings.ToLower(k)] = v
		}
	}
	for k := range ikv {
		if !validKeyFn(k, validKeys) {
			return nil, probe.NewError(errors.New("Options should be key-value pairs in the form key=value,... where valid key(s) are " + fmtStringCustom(validAbbrKeys, validKeys)))
		}
	}
	return ikv, nil
}

// gets the input serialization opts from cli context and constructs a map of csv, json or parquet options
func getInputSerializationOptsCustom(ctx *cli.Context) map[string]map[string]string {
	icsv := ctx.String("csv-input")
	ijson := ctx.String("json-input")
	m := make(map[string]map[string]string)

	csvType := ctx.IsSet("csv-input")
	jsonType := ctx.IsSet("json-input")
	if csvType && jsonType {
		fatalIf(errInvalidArgument(), "Only one of --csv-input or --json-input can be specified as input serialization option")
	}

	if icsv != "" {
		kv, err := parseSerializationOptsCustom(icsv, append(validCSVCommonKeysCustom, validCSVInputKeysCustom...), validCSVInputAbbrKeysCustom)
		fatalIf(err, "Invalid serialization option(s) specified for --csv-input flag")

		m["csv"] = kv
	}
	if ijson != "" {
		kv, err := parseSerializationOptsCustom(ijson, validJSONInputKeysCustom, nil)

		fatalIf(err, "Invalid serialization option(s) specified for --json-input flag")
		m["json"] = kv
	}

	return m
}

// gets the output serialization opts from cli context and constructs a map of csv or json options
func getOutputSerializationOptsCustom(ctx *cli.Context, csvHdrs []string) (opts map[string]map[string]string) {
	m := make(map[string]map[string]string)

	ocsv := ctx.String("csv-output")
	ojson := ctx.String("json-output")
	csvType := ctx.IsSet("csv-output")
	jsonType := ctx.IsSet("json-output")

	if csvType && jsonType {
		fatalIf(errInvalidArgument(), "Only one of --csv-output, or --json-output can be specified as output serialization option")
	}

	if jsonType && len(csvHdrs) > 0 {
		fatalIf(errInvalidArgument(), "--csv-output-header incompatible with --json-output option")
	}

	if csvType {
		validKeys := append(validCSVCommonKeysCustom, validJSONCSVCommonOutputKeysCustom...)
		kv, err := parseSerializationOptsCustom(ocsv, append(validKeys, validCSVOutputKeys...), validCSVOutputAbbrKeys)
		fatalIf(err, "Invalid value(s) specified for --csv-output flag")
		m["csv"] = kv
	}

	if jsonType || globalJSON {
		kv, err := parseSerializationOptsCustom(ojson, validJSONCSVCommonOutputKeysCustom, validJSONOutputAbbrKeysCustom)
		fatalIf(err, "Invalid value(s) specified for --json-output flag")
		m["json"] = kv
	}
	return m
}

// getCSVHeaderCustom fetches the first line of csv query object
func getCSVHeaderCustom(sourceURL string, encKeyDB map[string][]prefixSSEPair) ([]string, *probe.Error) {
	var r io.ReadCloser
	switch sourceURL {
	case "-":
		r = os.Stdin
	default:
		var err *probe.Error
		var metadata map[string]string
		if r, metadata, err = getSourceStreamMetadataFromURL(globalContext, sourceURL, "", time.Time{}, encKeyDB); err != nil {
			return nil, err.Trace(sourceURL)
		}
		ctype := metadata["Content-Type"]
		if strings.Contains(ctype, "gzip") {
			var e error
			r, e = gzip.NewReader(r)
			if e != nil {
				return nil, probe.NewError(e)
			}
			defer r.Close()
		} else if strings.Contains(ctype, "bzip") {
			defer r.Close()
			r = ioutil.NopCloser(bzip2.NewReader(r))
		} else {
			defer r.Close()
		}
	}
	br := bufio.NewReader(r)
	line, _, err := br.ReadLine()
	if err != nil {
		return nil, probe.NewError(err)
	}
	return strings.Split(string(line), ","), nil
}

// returns true if query is selectign all columns of the csv object
func isSelectAllCustom(query string) bool {
	match, _ := regexp.MatchString("^\\s*?select\\s+?\\*\\s+?.*?$", query)
	return match
}

// if csv-output-header is set to a comma delimited string use it, othjerwise attempt to get the header from
// query object
func getCSVOutputHeadersCustom(ctx *cli.Context, url string, encKeyDB map[string][]prefixSSEPair, query string) (hdrs []string) {
	if !ctx.IsSet("csv-output-header") {
		return
	}

	hdrStr := ctx.String("csv-output-header")
	if hdrStr == "" && isSelectAllCustom(query) {
		// attempt to get the first line of csv as header
		if hdrs, err := getCSVHeaderCustom(url, encKeyDB); err == nil {
			return hdrs
		}
	}
	hdrs = strings.Split(hdrStr, ",")
	return
}

// get the Select options for custom select API
func getCustomOpts(ctx *cli.Context, csvHdrs []string) (s SelectObjectOpts) {
	is := getInputSerializationOptsCustom(ctx)
	os := getOutputSerializationOptsCustom(ctx, csvHdrs)

	return SelectObjectOpts{
		InputSerOpts:    is,
		OutputSerOpts:   os,
		CompressionType: minio.SelectCompressionType(ctx.String("compression")),
	}
}

func isCSVOrJSONCustom(inOpts map[string]map[string]string) bool {
	if _, ok := inOpts["csv"]; ok {
		return true
	}
	if _, ok := inOpts["json"]; ok {
		return true
	}
	return false
}

func customSelect(targetURL, expression string, encKeyDB map[string][]prefixSSEPair, selOpts SelectObjectOpts, csvHdrs []string, writeHdr bool) *probe.Error {
	ctx, cancelSelect := context.WithCancel(globalContext)
	defer cancelSelect()

	alias, _, _, err := expandAlias(targetURL)
	if err != nil {
		return err.Trace(targetURL)
	}

	targetClnt, err := newClient(targetURL)
	if err != nil {
		return err.Trace(targetURL)
	}

	sseKey := getSSE(targetURL, encKeyDB[alias])
	outputer, err := targetClnt.Select(ctx, expression, sseKey, selOpts)
	if err != nil {
		return err.Trace(targetURL, expression)
	}
	defer outputer.Close()

	// write csv header to stdout
	if len(csvHdrs) > 0 && writeHdr {
		fmt.Println(strings.Join(csvHdrs, ","))
	}
	_, e := io.Copy(os.Stdout, outputer)
	return probe.NewError(e)
}

func validateOptsCustom(selOpts SelectObjectOpts, url string) {
	_, targetURL, _ := mustExpandAlias(url)
	if strings.HasSuffix(targetURL, ".parquet") && isCSVOrJSONCustom(selOpts.InputSerOpts) {
		fatalIf(errInvalidArgument(), "Input serialization flags --csv-input and --json-input cannot be used for object in .parquet format")
	}
}

// validate args and optionally fetch the csv header of query object
func getAndValidateArgsCustom(ctx *cli.Context, encKeyDB map[string][]prefixSSEPair, url string) (query string, csvHdrs []string, selOpts SelectObjectOpts) {
	query = ctx.String("query")
	csvHdrs = getCSVOutputHeadersCustom(ctx, url, encKeyDB, query)
	selOpts = getCustomOpts(ctx, csvHdrs)
	validateOptsCustom(selOpts, url)
	return
}

// check custom input arguments.
func checkCustomSyntax(ctx *cli.Context) {
	if len(ctx.Args()) == 0 {
		cli.ShowCommandHelpAndExit(ctx, "custom", 1) // last argument is exit code.
	}
}

// mainCustom is the main entry point for custom command.
func mainCustom(cliCtx *cli.Context) error {
	ctx, cancelCustom := context.WithCancel(globalContext)
	defer cancelCustom()

	var (
		csvHdrs []string
		selOpts SelectObjectOpts
		query   string
	)
	// Parse encryption keys per command.
	encKeyDB, err := getEncKeys(cliCtx)
	fatalIf(err, "Unable to parse encryption keys.")

	// validate custom input arguments.
	checkCustomSyntax(cliCtx)
	// extract URLs.
	URLs := cliCtx.Args()
	writeHdr := true
	for _, url := range URLs {
		if _, targetContent, err := url2Stat(ctx, url, "", false, encKeyDB, time.Time{}); err != nil {
			errorIf(err.Trace(url), "Unable to run custom for "+url+".")
			continue
		} else if !targetContent.Type.IsDir() {
			if writeHdr {
				query, csvHdrs, selOpts = getAndValidateArgsCustom(cliCtx, encKeyDB, url)
			}
			errorIf(customSelect(url, query, encKeyDB, selOpts, csvHdrs, writeHdr).Trace(url), "Unable to run custom")
			writeHdr = false
			continue
		}
		targetAlias, targetURL, _ := mustExpandAlias(url)
		clnt, err := newClientFromAlias(targetAlias, targetURL)
		if err != nil {
			errorIf(err.Trace(url), "Unable to initialize target `"+url+"`.")
			continue
		}

		for content := range clnt.List(ctx, ListOptions{isRecursive: cliCtx.Bool("recursive"), showDir: DirNone}) {
			if content.Err != nil {
				errorIf(content.Err.Trace(url), "Unable to list on target `"+url+"`.")
				continue
			}
			if writeHdr {
				query, csvHdrs, selOpts = getAndValidateArgsCustom(cliCtx, encKeyDB, targetAlias+content.URL.Path)
			}
			contentType := mimedb.TypeByExtension(filepath.Ext(content.URL.Path))
			for _, cTypeSuffix := range supportedContentTypes {
				if strings.Contains(contentType, cTypeSuffix) {
					errorIf(customSelect(targetAlias+content.URL.Path, query,
						encKeyDB, selOpts, csvHdrs, writeHdr).Trace(content.URL.String()), "Unable to run custom")
				}
				writeHdr = false
			}
		}
	}

	// Done.
	return nil
}
