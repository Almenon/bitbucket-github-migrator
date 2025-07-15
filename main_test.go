package main

import (
	"os"
	"testing"

	"github.com/go-test/deep"
)

func TestParseRepos(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "TestParseRepos")
	if err != nil {
		t.Error("Failed to create temp file")
	}
	defer file.Close()
	_, err = file.WriteString(` repo1 
	
	repo2
	repo/With+Invalid&Chars
	#commented out repo
	repo(4)
	`)
	if err != nil {
		t.Error("Failed to write to file")
	}

	result := parseRepos(file.Name())

	want := []string{"repo1", "repo2", "repo-With-Invalid-Chars", "repo-4"}
	if diff := deep.Equal(result, want); diff != nil {
		t.Error(diff)
	}
}
