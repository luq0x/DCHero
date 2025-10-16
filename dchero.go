package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	red   = "\x1b[31m"
	reset = "\x1b[0m"
)

var (
	manifestRe = regexp.MustCompile(`(?i)(?:^|/)(package\.json|package-lock\.json|yarn\.lock|pnpm-lock\.yaml|requirements\.txt|pyproject\.toml|Pipfile|Pipfile\.lock|constraints\.txt|setup\.py|composer\.json|go\.mod)(?:$|[?#/])`)
	reqSplitRe = regexp.MustCompile(`[<>=!~\[\];\s]`)

	importReqRe = regexp.MustCompile(`(?:require\(\s*['"]([^'"]+)['"]\s*\))|(?:import\s+(?:.+?\s+from\s+)?['"]([^'"]+)['"])`)
	scopedRe    = regexp.MustCompile(`@[\w.-]+\/[\w.-]+`)

	httpClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	npmURL  = "https://registry.npmjs.org/%s/"
	pypiURL = "https://pypi.org/project/%s/"

	headCache = make(map[string]int)
	headMu    sync.Mutex
)

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0",
}

func init() { rand.Seed(time.Now().UnixNano()) }

func randomUA() string { return userAgents[rand.Intn(len(userAgents))] }

func looksLikeCodeFile(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".js") || strings.HasSuffix(l, ".mjs") || strings.HasSuffix(l, ".cjs") || strings.HasSuffix(l, ".ts")
}

func filterManifestURLs(lines []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		p, err := url.Parse(u)
		if err != nil {
			continue
		}
		if p.Scheme != "http" && p.Scheme != "https" {
			continue
		}
		if p.Host == "" {
			continue
		}
		pathPlus := p.Path
		if p.RawQuery != "" {
			pathPlus += "?" + p.RawQuery
		}
		unesc, _ := url.PathUnescape(pathPlus)
		if manifestRe.MatchString(unesc) || looksLikeCodeFile(unesc) {
			seen[u] = struct{}{}
			out = append(out, u)
		}
	}
	return out
}

func httpGET(u string, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

func httpHEAD(u string, headers map[string]string) (int, error) {
	req, err := http.NewRequest(http.MethodHead, u, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	headMu.Lock()
	if st, ok := headCache[u]; ok {
		headMu.Unlock()
		return st, nil
	}
	headMu.Unlock()

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	headMu.Lock()
	headCache[u] = resp.StatusCode
	headMu.Unlock()
	return resp.StatusCode, nil
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type language string

const (
	langJS     language = "js"
	langPython language = "python"
)

func getDependencies(targetURL string) (deps []string, lang language, err error) {
	h := map[string]string{"User-Agent": randomUA()}
	body, _, err := httpGET(targetURL, h)
	if err != nil {
		return nil, "", err
	}

	if strings.EqualFold(path.Base(targetURL), "package.json") {
		var pj packageJSON
		if err := json.Unmarshal(body, &pj); err != nil {
			return nil, "", err
		}
		for k := range pj.Dependencies {
			deps = append(deps, k)
		}
		for k := range pj.DevDependencies {
			deps = append(deps, k)
		}
		return deps, langJS, nil
	}

	if looksLikeCodeFile(targetURL) {
		jsDeps := extractPackagesFromJS(string(body))
		if len(jsDeps) > 0 {
			return jsDeps, langJS, nil
		}
	}

	lines := strings.Split(string(body), "\n")
	for _, ln := range lines {
		line := strings.TrimSpace(ln)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := reqSplitRe.Split(line, -1)
		if len(parts) > 0 {
			pkg := strings.TrimSpace(parts[0])
			if pkg != "" {
				deps = append(deps, pkg)
			}
		}
	}
	return deps, langPython, nil
}

func extractPackagesFromJS(content string) []string {
	set := map[string]struct{}{}

	for _, m := range scopedRe.FindAllString(content, -1) {
		if strings.HasPrefix(m, ".") || strings.HasPrefix(m, "/") {
			continue
		}
		set[m] = struct{}{}
	}

	for _, sub := range importReqRe.FindAllStringSubmatch(content, -1) {
		var pkg string
		if sub[1] != "" {
			pkg = sub[1]
		} else if sub[2] != "" {
			pkg = sub[2]
		}
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		if strings.HasPrefix(pkg, ".") || strings.HasPrefix(pkg, "/") {
			continue
		}
		lpkg := strings.ToLower(pkg)
		if strings.HasPrefix(lpkg, "http://") || strings.HasPrefix(lpkg, "https://") || strings.HasPrefix(lpkg, "git+") {
			continue
		}
		set[pkg] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type vuln struct {
	Package  string
	Status   int
	Language language
}

func isUnclaimed(pkg string, lang language) (bool, int) {
	var checkURL string
	switch lang {
	case langJS:
		checkURL = fmt.Sprintf(npmURL, pkg)
	default:
		checkURL = fmt.Sprintf(pypiURL, pkg)
	}

	headMu.Lock()
	if st, ok := headCache[checkURL]; ok {
		headMu.Unlock()
		if st != http.StatusOK && st != http.StatusFound {
			return true, st
		}
		return false, st
	}
	headMu.Unlock()

	status, err := httpHEAD(checkURL, map[string]string{"User-Agent": randomUA()})
	if err != nil {
		return false, 0
	}
	if status != http.StatusOK && status != http.StatusFound {
		return true, status
	}
	return false, status
}

func runWorkers[T any, R any](inputs []T, worker func(T) (R, error), concurrency int) ([]R, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	type item struct {
		i int
		t T
	}
	type out struct {
		i   int
		val R
		err error
	}
	inCh := make(chan item)
	outCh := make(chan out)
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	go func() {
		for idx, v := range inputs {
			inCh <- item{i: idx, t: v}
		}
		close(inCh)
	}()

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range inCh {
				sem <- struct{}{}
				val, err := worker(it.t)
				<-sem
				outCh <- out{i: it.i, val: val, err: err}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(outCh)
	}()

	results := make([]R, 0, len(inputs))
	var firstErr error
	for o := range outCh {
		if o.err != nil && firstErr == nil {
			firstErr = o.err
		}
		results = append(results, o.val)
	}
	return results, firstErr
}

func checkURLDependencies(targetURL string, threads int) ([]vuln, error) {
	deps, lang, err := getDependencies(targetURL)
	if err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return nil, nil
	}

	type inp struct{ name string }
	type outp struct{ v *vuln }

	inputs := make([]inp, 0, len(deps))
	seen := make(map[string]struct{})
	for _, d := range deps {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		inputs = append(inputs, inp{name: d})
	}

	worker := func(x inp) (outp, error) {
		isV, code := isUnclaimed(x.name, lang)
		if isV {
			return outp{v: &vuln{Package: x.name, Status: code, Language: lang}}, nil
		}
		return outp{v: nil}, nil
	}

	outs, _ := runWorkers(inputs, worker, threads)

	var vulns []vuln
	for _, o := range outs {
		if o.v != nil {
			vulns = append(vulns, *o.v)
		}
	}
	return vulns, nil
}

func printBanner() {
	const banner = `
 (             )     (       )   
 )\ )   (   ( /(     )\ ) ( /(   
(()/(   )\  )\())(  (()/( )\())  
 /(_))(((_)((_)\ )\  /(_)|(_)\   
(_))_ )\___ _((_|(_)(_))   ((_)  
 |   ((/ __| || | __| _ \ / _ \  
 | |) | (__| __ | _||   /| (_) | 
 |___/ \___|_||_|___|_|_\ \___/  

`
	fmt.Printf("%s%s%s", red, banner, reset)
}

func main() {
	silent := flag.Bool("silent", false, "suppress banner output")
	threads := flag.Int("t", 20, "number of threads (1-100)")
	flag.Parse()

	if *threads < 1 {
		*threads = 1
	}
	if *threads > 100 {
		*threads = 100
	}

	if !*silent {
		printBanner()
	}

	var raw []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			raw = append(raw, line)
		}
	}
	// silencioso mesmo se der erro de leitura
	if len(raw) == 0 {
		// sem prints de erro
		return
	}

	filtered := filterManifestURLs(raw)
	if len(filtered) == 0 {
		// sem prints de erro
		return
	}

	type inp struct{ u string }
	type outp struct {
		u     string
		vulns []vuln
		err   error
	}
	inputs := make([]inp, 0, len(filtered))
	for _, u := range filtered {
		inputs = append(inputs, inp{u: u})
	}
	worker := func(x inp) (outp, error) {
		vv, err := checkURLDependencies(x.u, *threads)
		return outp{u: x.u, vulns: vv, err: err}, nil
	}
	results, _ := runWorkers(inputs, worker, *threads)

	for _, r := range results {
		// não imprime erros; ignora silently
		if r.err != nil {
			continue
		}
		for _, v := range r.vulns {
			tag := fmt.Sprintf("%s[%s|%d|%s]%s", red, v.Package, v.Status, v.Language, reset)
			fmt.Printf("%s %s\n", tag, r.u)
		}
	}
}
