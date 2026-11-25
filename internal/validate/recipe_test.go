package validate

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func text(s string) *string { return &s }

func TestRecipe(t *testing.T) {
	yes := true
	ok := api.Recipe{Steps: []api.RecipeStep{
		{Package: []string{"nginx", "py311-requests"}},
		{File: &api.RecipeFile{Path: "/usr/local/etc/nginx/nginx.conf", Content: text("x\n"), Mode: "0644", Owner: "root", Group: "wheel"}},
		{File: &api.RecipeFile{Path: "/etc/sysctl.conf", Content: text("")}},
		{File: &api.RecipeFile{Path: "/usr/local/etc/ssh/authorized_keys/deploy", Content: text("ssh-ed25519 AAAA key\n"), Mode: "0644"}},
		{Sysrc: map[string]string{"nginx_enable": "YES"}},
		{Sysrc: map[string]string{"daily_clean_tmps_enable": "YES"}, In: "periodic.conf"},
		{Service: &api.RecipeService{Name: "nginx", Enabled: &yes}},
		{Run: []string{"pw", "useradd", "deploy"}, Unless: []string{"pw", "usershow", "deploy"}, Creates: "/home/deploy"},
	}}
	if err := Recipe(ok); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", RecipeFileMax+1)
	bad := map[string]api.Recipe{
		"empty":            {},
		"two kinds":        {Steps: []api.RecipeStep{{Package: []string{"a"}, Run: []string{"b"}}}},
		"no kind":          {Steps: []api.RecipeStep{{}}},
		"package flag":     {Steps: []api.RecipeStep{{Package: []string{"-y"}}}},
		"source in daemon": {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/etc/a", Source: "a"}}}},
		"no content":       {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/etc/a"}}}},
		"relative path":    {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "usr/local/a", Content: text("")}}}},
		"dotdot":           {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/../../etc/passwd", Content: text("")}}}},
		"authorized keys":  {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/etc/ssh/authorized_keys/deploy", Content: text("")}}}},
		"etc ssh other":    {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/etc/ssh/ssh_host_ed25519_key", Content: text("")}}}},
		"etc":              {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/etc/passwd", Content: text("")}}}},
		"etc rc.conf":      {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/etc/rc.conf", Content: text("")}}}},
		"etc dir itself":   {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/etc", Content: text("")}}}},
		"usr bin":          {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/bin/x", Content: text("")}}}},
		"too big":          {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/a", Content: &big}}}},
		"binary":           {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/a", Content: text("a\x00b")}}}},
		"owner injection":  {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/a", Content: text(""), Owner: "root; id"}}}},
		"mode":             {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/a", Content: text(""), Mode: "u+s"}}}},
		"mode 4755":        {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/a", Content: text(""), Mode: "4755"}}}},
		"mode 1777":        {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/a", Content: text(""), Mode: "1777"}}}},
		"usr lib32":        {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/lib32/x", Content: text("")}}}},
		"usr libdata":      {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/libdata/x", Content: text("")}}}},
		"usr tests":        {Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/tests/x", Content: text("")}}}},
		"sysrc name":       {Steps: []api.RecipeStep{{Sysrc: map[string]string{"A B": "x"}}}},
		"sysrc newline":    {Steps: []api.RecipeStep{{Sysrc: map[string]string{"a_enable": "x\ny"}}}},
		"sysrc file":       {Steps: []api.RecipeStep{{Sysrc: map[string]string{"a_enable": "YES"}, In: "rc.conf.local"}}},
		"in without sysrc": {Steps: []api.RecipeStep{{Run: []string{"a"}, In: "rc.conf"}}},
		"service name":     {Steps: []api.RecipeStep{{Service: &api.RecipeService{Name: "a b", Running: &yes}}}},
		"service nothing":  {Steps: []api.RecipeStep{{Service: &api.RecipeService{Name: "nginx"}}}},
		"empty argv":       {Steps: []api.RecipeStep{{Run: []string{}}}},
		"guard on file":    {Steps: []api.RecipeStep{{Package: []string{"a"}, Creates: "/x"}}},
		"creates relative": {Steps: []api.RecipeStep{{Run: []string{"a"}, Creates: "x"}}},
	}
	for name, r := range bad {
		if err := Recipe(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// the files of one recipe are limited together
	var many api.Recipe
	chunk := strings.Repeat("x", RecipeFileMax)
	for i := 0; i < 9; i++ {
		many.Steps = append(many.Steps, api.RecipeStep{File: &api.RecipeFile{Path: "/usr/local/f" + string(rune('a'+i)), Content: &chunk}})
	}
	if err := Recipe(many); err == nil {
		t.Error("nine MiB of files were accepted")
	}
}

func TestRecipeRequirements(t *testing.T) {
	steps := []api.RecipeStep{{Run: []string{"echo", "x"}}}
	req := func(check []string, msg string) api.Recipe {
		return api.Recipe{Requirements: []api.RecipeRequirement{{Check: check, Message: msg}}, Steps: steps}
	}
	if err := Recipe(req([]string{"test", "-x", "/usr/sbin/sshd"}, "sshd is not installed")); err != nil {
		t.Fatal(err)
	}
	var many []api.RecipeRequirement
	for i := 0; i <= RecipeMaxRequirements; i++ {
		many = append(many, api.RecipeRequirement{Check: []string{"true"}, Message: "m"})
	}
	for name, r := range map[string]api.Recipe{
		"no check":      req(nil, "m"),
		"empty check":   req([]string{}, "m"),
		"empty command": req([]string{""}, "m"),
		"nul in a word": req([]string{"a\x00b"}, "m"),
		"no message":    req([]string{"true"}, ""),
		"blank message": req([]string{"true"}, "  "),
		"two lines":     req([]string{"true"}, "a\nb"),
		"long message":  req([]string{"true"}, strings.Repeat("x", 501)),
		"too many":      {Requirements: many, Steps: steps},
	} {
		err := Recipe(r)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "requirements") {
			t.Errorf("%s: the error does not name the field: %v", name, err)
		}
	}
	// a recipe of requirements alone still needs a step
	if err := Recipe(api.Recipe{Requirements: req([]string{"true"}, "m").Requirements}); err == nil {
		t.Error("a recipe without steps was accepted")
	}
}
