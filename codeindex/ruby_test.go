package codeindex_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/codeindex"
)

func TestRubyRailsAndSorbetNavigation(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	files := map[string]string{
		"app/models/user.rb": `class User < ApplicationRecord
  has_many :posts
  scope :active, -> { where(active: true) }
  before_save :normalize
  attr_accessor :nickname
  sig { params(key: Symbol, value: String).void }
  def []=(key, value); end
  def valid?; true; end
end
`,
		"sorbet/rbi/user.rbi":        "module API\n  class User\n    sig { returns(String) }\n    def display_name; end\n  end\nend\n",
		"config/routes.rb":           "Rails.application.routes.draw do\n  namespace :admin do\n    resources :users\n  end\nend\n",
		"db/migrate/create_users.rb": "class CreateUsers < ActiveRecord::Migration[8.0]\n  def change; create_table :users; end\nend\n",
		"Gemfile":                    "source 'https://rubygems.org'\ngem 'rails'\n",
		"Rakefile":                   "require_relative 'config/application'\nRails.application.load_tasks\n",
		"config.ru":                  "require_relative 'config/environment'\nrun Rails.application\n",
		"lib/tasks/clean.rake":       "task :clean do\n  puts 'clean'\nend\n",
		"example.gemspec":            "Gem::Specification.new do |spec|\n  spec.name = 'example'\nend\n",
		"other.go":                   "package p\nfunc Other() {}\n",
	}
	for name, source := range files {
		write(t, root, name, source)
	}
	for _, name := range []string{"app/views/index.html.erb", "vendor/gems/dependency.rb", "ignored.rb", "sorbet/rbi/ignored.rbi"} {
		write(t, root, name, "class Ignored; end\n")
	}
	write(t, root, ".gitignore", "ignored.rb\n")
	write(t, root, "sorbet/.gitignore", "ignored.rbi\n")
	index := codeindex.New(root, cache)
	first, err := index.Map(t.Context(), codeindex.Query{Language: "ruby", Depth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != len(files)-1 || first.Status.Parsed != len(files) || first.Status.Issues != 0 {
		t.Fatalf("initial map: %+v", first)
	}
	for _, entry := range first.Entries {
		if entry.Language != "ruby" || len(entry.SHA256) != 64 {
			t.Fatalf("file entry: %+v", entry)
		}
	}
	for _, tt := range []struct{ query, kind, path, container string }{
		{"posts", "association", "app/models/user.rb", "User"},
		{"active", "scope", "app/models/user.rb", "User"},
		{"normalize", "callback", "app/models/user.rb", "User"},
		{"nickname", "attribute", "app/models/user.rb", "User"},
		{"[]=", "method", "app/models/user.rb", "User"},
		{"valid?", "method", "app/models/user.rb", "User"},
		{"display_name", "method", "sorbet/rbi/user.rbi", "API::User"},
		{"API", "module", "sorbet/rbi/user.rbi", ""},
		{"users", "route", "config/routes.rb", "admin"},
		{"change", "method", "db/migrate/create_users.rb", "CreateUsers"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			result, err := index.Find(t.Context(), codeindex.Query{Language: "ruby", Kind: tt.kind, Text: tt.query})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Entries) != 1 || result.Status.Reused != len(files) {
				t.Fatalf("find: %+v", result)
			}
			e := result.Entries[0]
			if e.Name != tt.query || e.Match != "exact_name" || e.Path != tt.path || e.Container != tt.container || e.Span == nil {
				t.Fatalf("entry: %+v", e)
			}
			if (tt.query == "display_name" || tt.query == "[]=") && !strings.Contains(e.Signature, "sig {") {
				t.Fatalf("missing Sorbet signature: %+v", e)
			}
			mapped, err := index.Map(t.Context(), codeindex.Query{Path: tt.path, Language: "ruby", Kind: tt.kind})
			if err != nil || len(mapped.Entries) == 0 {
				t.Fatalf("kind map: %+v, %v", mapped, err)
			}
		})
	}

	hash := fileHash(t, index, "app/models/user.rb")
	write(t, root, "app/models/user.rb", "class User\n  has_many :articles\nend\n")
	updated, err := codeindex.New(root, cache).Find(t.Context(), codeindex.Query{Text: "articles", Kind: "association"})
	if err != nil || len(updated.Entries) != 1 || updated.Status.Parsed != 1 || fileHash(t, index, "app/models/user.rb") == hash {
		t.Fatalf("refresh: %+v, %v", updated, err)
	}
	if err := os.Remove(filepath.Join(root, "sorbet/rbi/user.rbi")); err != nil {
		t.Fatal(err)
	}
	deleted, err := index.Find(t.Context(), codeindex.Query{Text: "display_name"})
	if err != nil || len(deleted.Entries) != 0 {
		t.Fatalf("deleted RBI: %+v, %v", deleted, err)
	}
}

func TestRubyParserDiagnosticsRetainSourceSearch(t *testing.T) {
	root := t.TempDir()
	write(t, root, "broken.rb", "class Good; end\n# unique lexical needle\ndef broken(\n")
	index := codeindex.New(root, t.TempDir())
	result, err := index.Find(t.Context(), codeindex.Query{Text: "unique lexical needle", Language: "ruby"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status.Issues != 1 || len(result.Entries) != 1 || result.Entries[0].Match != "source" {
		t.Fatalf("fallback: %+v", result)
	}
}
