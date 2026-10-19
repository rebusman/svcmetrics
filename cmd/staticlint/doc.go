// Staticlint is a multichecker that runs the standard analyzers of
// golang.org/x/tools, the staticcheck checks, errcheck, ineffassign and the
// noosexit analyzer.
//
// # Usage
//
//	go build -o staticlint ./cmd/staticlint
//	./staticlint [flags] ./...
//
// Packages are analyzed together with their tests. The exit status is 0
// without findings, 3 with findings and 1 on errors; go run reports any
// non-zero status as 1.
//
// The flags are those of [golang.org/x/tools/go/analysis/multichecker]:
//
//	help [NAME]   list the analyzers or describe one
//	-NAME         run only the named analyzers
//	-NAME=false   run all analyzers except NAME
//	-NAME.FLAG=V  set a flag of an analyzer
//	-test=false   skip test files
//	-json         print findings as JSON
//	-fix          apply suggested fixes
//
// //lint:ignore and //nolint directives are not supported.
//
// # Standard analyzers
//
// From [golang.org/x/tools/go/analysis/passes]:
//
//   - appends: append calls with a single argument.
//   - asmdecl: assembly that does not match its Go declaration.
//   - assign: useless assignments such as x = x.
//   - atomic: misuse of sync/atomic, as in x = atomic.AddInt64(&x, 1).
//   - bools: redundant or suspicious boolean expressions.
//   - buildtag: malformed //go:build and // +build lines.
//   - cgocall: violations of the cgo pointer passing rules.
//   - composites: unkeyed composite literals of imported types.
//   - copylocks: locks passed or copied by value.
//   - deepequalerrors: reflect.DeepEqual on error values.
//   - defers: defer log.Println(time.Since(start)) and similar.
//   - directive: misplaced or misspelled toolchain directives.
//   - errorsas: invalid second argument of errors.As.
//   - framepointer: assembly that clobbers the frame pointer.
//   - hostport: addresses built with fmt.Sprintf instead of net.JoinHostPort.
//   - httpmux: ServeMux patterns newer than the declared Go version.
//   - httpresponse: use of an http.Response before checking the error.
//   - ifaceassert: impossible interface-to-interface assertions.
//   - loopclosure: loop variables captured by goroutines or defers.
//   - lostcancel: context cancel functions that are not called.
//   - nilfunc: comparisons of functions with nil.
//   - nilness: nil dereferences and impossible nil comparisons.
//   - printf: Printf format strings that do not match the arguments.
//   - reflectvaluecompare: reflect.Value compared with == or DeepEqual.
//   - shift: shifts that exceed the integer width.
//   - sigchanyzer: unbuffered channels passed to signal.Notify.
//   - slog: mismatched key-value pairs in log/slog calls.
//   - sortslice: sort.Slice called with a non-slice.
//   - sqlrowserr: rows iterated without checking rows.Err.
//   - stdmethods: wrong signatures of well-known methods such as String.
//   - stdversion: symbols newer than the declared Go version.
//   - stringintconv: string(int) conversions.
//   - structtag: malformed struct tags.
//   - testinggoroutine: t.Fatal called from a goroutine.
//   - tests: malformed tests, benchmarks, fuzz tests and examples.
//   - timeformat: the 2006-02-01 layout.
//   - unmarshal: non-pointer values passed to unmarshal functions.
//   - unreachable: unreachable code.
//   - unsafeptr: invalid uintptr to unsafe.Pointer conversions.
//   - unusedresult: unused results of pure functions such as fmt.Sprintf.
//   - unusedwrite: writes to fields and elements that are never read.
//   - waitgroup: WaitGroup.Add called inside the goroutine.
//
// # Staticcheck
//
// From [honnef.co/go/tools], described at https://staticcheck.dev/docs/checks/:
//
//   - SA, all checks: bugs and misuse of the standard library.
//   - S, all checks: code that can be simplified.
//   - ST, the checks enabled by default in staticcheck: style issues.
//
// # Public analyzers
//
//   - errcheck: unchecked errors; discard one explicitly with _ =.
//   - ineffassign: assignments whose value is never used.
//
// # noosexit
//
// Noosexit reports direct calls to os.Exit in the main function of package
// main, which skip the deferred calls of main.
package main
