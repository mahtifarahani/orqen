package testfakes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func headOfBranch(t *testing.T, g *GitLab, srv *httptest.Server, project string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+gitlabPrefix+"/projects/"+project+"/repository/branches/main", nil)
	req.Header.Set("PRIVATE-TOKEN", g.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, err)
	}
	return out.Commit.ID
}

// The branch head is stable until a file of that project changes, and only that project's head moves.
func TestGitLabBranchHeadMovesWithFileChanges(t *testing.T) {
	g := NewGitLab()
	g.Branches = []string{"main"}
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	first := headOfBranch(t, g, srv, "1")
	if first != "0123456789abcdef0123456789abcdef01234567" || headOfBranch(t, g, srv, "1") != first {
		t.Fatal("the head is stable while nothing changes")
	}
	g.SetProjectFile(1, "a.txt", "one")
	second := headOfBranch(t, g, srv, "1")
	if second == first || len(second) != 40 {
		t.Fatal("a file change moves the head", second)
	}
	g.SetProjectFile(1, "a.txt", "two")
	if third := headOfBranch(t, g, srv, "1"); third == second || third == first {
		t.Fatal("every change moves it again")
	}
	if headOfBranch(t, g, srv, "2") != first {
		t.Fatal("another project keeps its head")
	}
}

// With the smart-HTTP repositories linked, the branch head is the real sha of main there, so an archive of it works.
func TestGitLabBranchHeadIsTheRealCommitWhenReposAreLinked(t *testing.T) {
	repos, err := NewGitRepos(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	g := NewGitLab()
	g.Branches = []string{"main"}
	g.LinkRepos(repos)
	g.SeedGraph() // the seed files are initial content: they must not hide the real head
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	want := repos.Head("group-1/proj-1", "main")
	if len(want) != 40 || headOfBranch(t, g, srv, "1") != want {
		t.Fatal("the head must be the real commit of main", want)
	}
	g.SetProjectFile(1, "a.txt", "one")
	if headOfBranch(t, g, srv, "1") == want {
		t.Fatal("a file override still moves the head")
	}
}
