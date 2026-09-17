package syntax_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/codeindex/syntax"
)

func TestRubyLanguage(t *testing.T) {
	for _, path := range []string{"app/models/user.rb", "sig/user.rbi", "tasks/db.rake", "foo.gemspec", "Gemfile", "nested/Gemfile", "Rakefile", "config.ru", "UPPER.RB"} {
		if got := syntax.Language(path); got != "ruby" {
			t.Errorf("Language(%q) = %q", path, got)
		}
	}
	for _, path := range []string{"view.html.erb", "Gemfile.lock", "ruby.txt", "script", "notconfig.ru"} {
		if got := syntax.Language(path); got != "" {
			t.Errorf("Language(%q) = %q", path, got)
		}
	}
}

func TestRubyDeclarations(t *testing.T) {
	source := `# typed: strict
module Admin
  class User < ApplicationRecord
    ID = T.type_alias { String }
    DEFAULT = T.let(1, Integer)
    def valid?; true; end
    def save! = persist
    def []=(key, value); store(key, value); end
    def self.build(name); new(name); end
    class << self
      def find(id); lookup(id); end
    end
    def café
      local = 1
      def nested; end
      has_many :not_an_association
    end
  end
end
class Admin::Other; end
External::VALUE = 2
`
	out := extractRuby(t, source)
	var got []string
	for _, d := range out.Declarations {
		got = append(got, d.Kind+":"+d.Container+":"+d.Name)
		if d.Kind == "method" && (strings.Contains(d.Signature, "persist") || strings.Contains(d.Signature, "store(") || strings.Contains(d.Signature, "lookup(") || strings.Contains(d.Signature, "local")) {
			t.Errorf("body in signature: %+v", d)
		}
	}
	want := []string{"module::Admin", "class:Admin:User", "constant:Admin::User:ID", "constant:Admin::User:DEFAULT", "method:Admin::User:valid?", "method:Admin::User:save!", "method:Admin::User:[]=", "method:Admin::User.self:build", "method:Admin::User::<singleton:self>:find", "method:Admin::User:café", "class::Admin::Other", "constant::External::VALUE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("declarations = %v, want %v", got, want)
	}
}

func TestRubySorbetSignatures(t *testing.T) {
	source := `# typed: strict
class User
  extend T::Sig
  abstract!
  sig(:final) do
    params(name: String).returns(T.nilable(User))
  end
  # Preserve the signature through comments.
  def self.find(name); nil; end

  sig { returns(String) }
  private def label; "private"; end

  sig { void }
  unrelated_call
  def unsigned; end

  sig { returns(Integer) }
  def count = 42
end
`
	out := extractRuby(t, source)
	for _, d := range out.Declarations {
		switch d.Name {
		case "find":
			if !strings.Contains(d.Signature, "sig(:final) do") || !strings.Contains(d.Signature, "params(name: String)") || d.Span.StartLine != 5 || d.Span.EndLine != 9 {
				t.Errorf("Sorbet signature: %+v", d)
			}
		case "label":
			if !strings.Contains(d.Signature, "sig { returns(String) }") {
				t.Errorf("private signature: %+v", d)
			}
		case "unsigned":
			if d.Signature != "def unsigned" {
				t.Errorf("incorrectly attached signature: %+v", d)
			}
		case "count":
			if !strings.Contains(d.Signature, "sig { returns(Integer) }") || strings.Contains(d.Signature, "42") {
				t.Errorf("endless signature: %+v", d)
			}
		}
	}
	if len(out.Declarations) != 5 {
		t.Fatalf("declarations: %+v", out.Declarations)
	}
}

func TestRubyRailsMacros(t *testing.T) {
	source := `module Publishable
  extend ActiveSupport::Concern
  included do
    belongs_to :author, class_name: "User"
    has_many :comments
    has_one :image
    has_and_belongs_to_many :tags
    scope :published, -> { where(published: true) }
    before_save :normalize, :validate_title, if: :changed?
    after_commit "notify"
    attr_reader :title, :"valid?"
    attr_writer :content
    attr_accessor :slug
  end
  class_methods do
    attr_reader :configuration
  end
end
class PostsController < ApplicationController
  before_action :authenticate_user!
  self.after_action :audit
  other.before_action :not_ours
  attr_reader dynamic_name
  attr_reader "#{dynamic}"
  scope "#{dynamic}", -> { all }
  unknown :not_a_macro
  something { has_many :not_declarative }
  def show
    before_action :not_a_declaration
  end
end
get "/not_a_route"
`
	out := extractRuby(t, source)
	var got []string
	for _, d := range out.Declarations {
		if d.Kind == "module" || d.Kind == "class" || d.Kind == "method" {
			continue
		}
		got = append(got, d.Kind+":"+d.Name)
		if !strings.Contains(d.Container, "Publishable") && d.Container != "PostsController" {
			t.Errorf("container: %+v", d)
		}
	}
	want := []string{"association:author", "association:comments", "association:image", "association:tags", "scope:published", "callback:normalize", "callback:validate_title", "callback:notify", "attribute:title", "attribute:valid?", "attribute:content", "attribute:slug", "attribute:configuration", "callback:authenticate_user!", "callback:audit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("macros = %v, want %v", got, want)
	}
}

func TestRubyRailsRoutes(t *testing.T) {
	source := `Rails.application.routes.draw do
  root "home#index"
  namespace :admin do
    resources :users do
      member do
        post :activate, as: :activation
      end
      resources :comments
    end
    scope module: :billing do
      get "invoices", to: "invoices#index"
    end
  end
  get "login" => "sessions#new"
  get dynamic_path, to: "dynamic#show"
  get "#{dynamic}", to: "dynamic#show"
  mount Sidekiq::Web => "/jobs", as: :jobs
  concern :commentable do
    resources :comments
  end
  other.get "not_a_route"
end
class Client
  def request; get "/users"; end
end
`
	out := extractRuby(t, source)
	var got []string
	for _, d := range out.Declarations {
		if d.Kind == "route" {
			got = append(got, d.Container+":"+d.Name)
			if strings.Contains(d.Signature, " do") || strings.Contains(d.Signature, "\n") {
				t.Errorf("route body in signature: %+v", d)
			}
		}
	}
	want := []string{":root", ":admin", "admin:users", "admin/users/member:activation", "admin/users:comments", "admin/billing:invoices", ":login", ":jobs", ":commentable", "concern:commentable:comments"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
}

func TestRubyDeclarationLimit(t *testing.T) {
	// A single macro can produce many entries, so the common limit must be
	// enforced after expansion as well as for ordinary declarations.
	e := newExtractor(t, "ruby")
	source := "class Many\n attr_reader " + strings.Repeat(":item,", 4000) + ":last\nend\n"
	if _, err := e.Extract(t.Context(), []byte(source)); err == nil || !strings.Contains(err.Error(), "4000 declaration limit") {
		t.Fatalf("macro declaration limit: %v", err)
	}
}

func TestRubyRouteVariants(t *testing.T) {
	out := extractRuby(t, `App.routes.draw do
  root to: "home#index"
  root to: "other#index", as: :other_root
  resources :posts, :photos
  resource :profile
  constraints subdomain: "api" do
    scope "/v1" do
      match "/health", to: "health#show", via: [:get, :post], as: "health"
      delete :session
    end
  end
  concerns :commentable
end
`)
	var names []string
	for _, d := range out.Declarations {
		if d.Kind != "route" {
			t.Fatalf("unexpected declaration: %+v", d)
		}
		names = append(names, d.Name)
		if d.Name == "health" && d.Container != "/v1" {
			t.Fatalf("scope container: %+v", d)
		}
	}
	want := []string{"root", "other_root", "photos", "posts", "profile", "/v1", "health", "session", "commentable"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("routes = %v, want %v", names, want)
	}
}

func TestRubyIncompleteAndReuse(t *testing.T) {
	e := newExtractor(t, "ruby")
	out, err := e.Extract(t.Context(), []byte("class Good; end\ndef broken(\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Diagnostics) == 0 || len(out.Declarations) == 0 || out.Declarations[0].Name != "Good" {
		t.Fatalf("partial source: %+v", out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Extract(ctx, []byte("class Old; end")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	out, err = e.Extract(t.Context(), []byte("class Fresh; end"))
	if err != nil || len(out.Declarations) != 1 || out.Declarations[0].Name != "Fresh" {
		t.Fatalf("reuse: %+v, %v", out, err)
	}
}

func extractRuby(t *testing.T, source string) syntax.Outline {
	t.Helper()
	e := newExtractor(t, "ruby")
	out, err := e.Extract(t.Context(), []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", out.Diagnostics)
	}
	for _, d := range out.Declarations {
		if d.Span.StartByte >= d.Span.EndByte || d.Span.EndByte > uint(len(source)) || !strings.Contains(source[d.Span.StartByte:d.Span.EndByte], d.Name) {
			t.Fatalf("invalid span: %+v", d)
		}
	}
	return out
}
