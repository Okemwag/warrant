package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeLLM struct {
	out    Model
	err    error
	gotSys string
	gotIn  string
	schema map[string]any
}

func (f *fakeLLM) Complete(_ context.Context, system, user string, schema map[string]any) ([]byte, error) {
	f.gotSys, f.gotIn, f.schema = system, user, schema
	if f.err != nil {
		return nil, f.err
	}
	return json.Marshal(f.out)
}

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func TestCompile_UsesStatedLimits(t *testing.T) {
	llm := &fakeLLM{out: Model{
		Name: "Running gear", MonthlyBudget: 120, PerPurchaseCap: 60, ConfirmAbove: 25,
		Categories: []string{"Running Gear", " "}, Merchants: []string{"Demo-Store"}, AskOnNewMerchant: true,
		MinReturnDays: 30, DurationDays: 30,
	}}
	d, err := New(llm).Compile(context.Background(), "Up to $120 a month on running gear...", now)
	if err != nil {
		t.Fatal(err)
	}
	p := d.Permission
	if p.MonthlyBudget != 12000 || p.PerPurchaseCap != 6000 || p.ConfirmAbove != 2500 || p.MinReturnDays != 30 {
		t.Fatalf("limits %+v", p)
	}
	if len(p.Categories) != 1 || p.Categories[0] != "running gear" || p.Merchants[0] != "demo-store" {
		t.Fatalf("lists %v %v", p.Categories, p.Merchants)
	}
	if len(d.Assumptions) != 0 {
		t.Fatalf("unexpected assumptions %v", d.Assumptions)
	}
	if err := p.Engine("perm_x", 1).Validate(); err != nil {
		t.Fatalf("draft doesn't validate: %v", err)
	}
	if llm.gotIn != "Up to $120 a month on running gear..." || llm.schema["additionalProperties"] != false {
		t.Fatal("prompt or schema not passed through")
	}
}

func TestCompile_FillsDefaultsAndSaysSo(t *testing.T) {
	d, err := New(&fakeLLM{out: Model{Question: "Which stores?"}}).Compile(context.Background(), "buy stuff for me", now)
	if err != nil {
		t.Fatal(err)
	}
	p := d.Permission
	if p.MonthlyBudget != Defaults.Monthly || p.PerPurchaseCap != Defaults.PerPurchase || p.ConfirmAbove != Defaults.ConfirmAbove {
		t.Fatalf("defaults %+v", p)
	}
	if len(d.Assumptions) < 4 || d.Question != "Which stores?" {
		t.Fatalf("assumptions %v question %q", d.Assumptions, d.Question)
	}
	if !p.AskOnNewMerchant {
		t.Fatal("no merchants named should mean ask on new merchant")
	}
	if err := p.Engine("perm_x", 1).Validate(); err != nil {
		t.Fatalf("draft doesn't validate: %v", err)
	}
}

func TestCompile_NeverLooserThanConsistent(t *testing.T) {
	// The model returns an impossible combination; the draft is tightened.
	d := FromModel(Model{MonthlyBudget: 50, PerPurchaseCap: 80, ConfirmAbove: 100, Categories: []string{"toys"}, MinReturnDays: 7, DurationDays: 10}, now)
	p := d.Permission
	if p.PerPurchaseCap != 5000 || p.ConfirmAbove != 5000 {
		t.Fatalf("not tightened: %+v", p)
	}
	if !strings.Contains(strings.Join(d.Assumptions, " "), "lowered") {
		t.Fatalf("no note about lowering: %v", d.Assumptions)
	}
}

func TestCompile_PropagatesErrors(t *testing.T) {
	if _, err := New(&fakeLLM{err: ErrRefused}).Compile(context.Background(), "x", now); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
}

func TestDollars(t *testing.T) {
	for in, want := range map[float64]int64{0: 0, -5: 0, 19.99: 1999, 0.005: 1, 100: 10000} {
		if got := dollars(in); int64(got) != want {
			t.Errorf("dollars(%v) = %d, want %d", in, got, want)
		}
	}
}
