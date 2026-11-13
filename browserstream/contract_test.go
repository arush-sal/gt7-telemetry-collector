package browserstream

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/telemetry"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	fixSession = "4f9c2e7a1b3d5f60718293a4b5c6d7e8"
	fixVersion = "v0.0.0-test"
)

var fixTime = time.Date(2026, 11, 14, 12, 0, 0, 123456789, time.UTC)

func frame(seq uint64, brake, throttle float32) *telemetryv1.TelemetryFrame {
	return &telemetryv1.TelemetryFrame{
		RecvTimestamp: timestamppb.New(fixTime),
		Sequence:      seq,
		PackageId:     10123,
		Source:        telemetryv1.Source_SOURCE_LIVE,
		Brake:         brake,
		Throttle:      throttle,
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func asAny(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(mustMarshal(t, v), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func mustProject(t *testing.T, f *telemetryv1.TelemetryFrame, dropped uint64) (Sample, int) {
	t.Helper()
	s, inv, err := Project(fixSession, f, dropped)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return s, inv
}

func keys(m map[string]any) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func TestGoldenFixtures(t *testing.T) {
	const dir = "testdata/v1/valid"
	b108, b17, b226 := float32(108)/2.55, float32(17)/2.55, float32(226)/2.55
	sampleCases := map[string]struct {
		f       *telemetryv1.TelemetryFrame
		dropped uint64
	}{
		"sample.brake-throttle.json": {frame(12345, b108, 0), 0},
		"sample.dropped.json":        {frame(12345, b108, 0), 7},
		"sample.gap.json": {func() *telemetryv1.TelemetryFrame {
			f := frame(12345, b108, 0)
			f.Gap = true
			return f
		}(), 0},
		"sample.large-sequence.json": {func() *telemetryv1.TelemetryFrame {
			f := frame(math.MaxUint64, b108, 0)
			f.PackageId = math.MaxUint32
			return f
		}(), 0},
		"sample.null-input.json": {frame(12345, float32(math.NaN()), 100), 0},
		"sample.overlap.json":    {frame(12345, b17, b226), 0},
		"sample.replay.json": {func() *telemetryv1.TelemetryFrame {
			f := frame(12345, b108, 0)
			f.Source = telemetryv1.Source_SOURCE_REPLAY
			return f
		}(), 0},
		"sample.unknown-field.json": {frame(12345, b108, 0), 0},
	}
	for name, c := range sampleCases {
		t.Run(name, func(t *testing.T) {
			s, _ := mustProject(t, c.f, c.dropped)
			want := readJSON(t, filepath.Join(dir, name))
			delete(want, "futureField") // forward-compat fixture: unknown field is not emitted by us
			if got := asAny(t, s); !reflect.DeepEqual(got, want) {
				t.Errorf("got %v\nwant %v", got, want)
			}
			var back Sample
			wireEqual(t, filepath.Join(dir, name), &back, s)
			validate(t, s)
		})
	}
	for _, c := range []struct {
		name string
		st   SourceState
	}{
		{"hello.live.json", StateLive},
		{"hello.no-steering.json", StateStale},
		{"hello.waiting.json", StateWaiting},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := NewHello(fixSession, fixVersion, c.st)
			got := asAny(t, h)
			if want := readJSON(t, filepath.Join(dir, c.name)); !reflect.DeepEqual(got, want) {
				t.Errorf("got %v\nwant %v", got, want)
			}
			var back Hello
			wireEqual(t, filepath.Join(dir, c.name), &back, h)
			validate(t, h)
		})
	}
	for _, c := range []struct {
		name   string
		state  SourceState
		reason string
	}{
		{"status.live.json", StateLive, ReasonFirstFrame},
		{"status.resumed.json", StateLive, ReasonResumed},
		{"status.stale.json", StateStale, ReasonStaleTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := NewStatus(fixSession, c.state, c.reason)
			got := asAny(t, st)
			if want := readJSON(t, filepath.Join(dir, c.name)); !reflect.DeepEqual(got, want) {
				t.Errorf("got %v\nwant %v", got, want)
			}
			var back Status
			wireEqual(t, filepath.Join(dir, c.name), &back, st)
			validate(t, st)
		})
	}
}

// TestUnknownFieldForwardCompat documents consumer behaviour (v1 consumers ignore unknown fields);
// it exercises encoding/json, not collector output.
func TestUnknownFieldForwardCompat(t *testing.T) {
	b, err := os.ReadFile("testdata/v1/valid/sample.unknown-field.json")
	if err != nil {
		t.Fatal(err)
	}
	var s Sample
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal with unknown field: %v", err)
	}
	if s.Sequence != "12345" || s.Inputs.Brake == nil {
		t.Errorf("decoded sample wrong: %+v", s)
	}
}

func TestLargeSequence(t *testing.T) {
	for _, seq := range []uint64{math.MaxUint64, 1<<53 + 1} {
		s, _ := mustProject(t, frame(seq, 1, 1), 0)
		if s.Sequence != strconv.FormatUint(seq, 10) {
			t.Errorf("sequence = %q", s.Sequence)
		}
		var back Sample
		if err := json.Unmarshal(mustMarshal(t, s), &back); err != nil {
			t.Fatal(err)
		}
		got, err := strconv.ParseUint(back.Sequence, 10, 64)
		if err != nil || got != seq {
			t.Errorf("round trip %d -> %d (%v)", seq, got, err)
		}
	}
	if s, _ := mustProject(t, frame(math.MaxUint64, 1, 1), 0); s.Sequence != "18446744073709551615" {
		t.Errorf("max = %q", s.Sequence)
	}
}

func TestInvalidValuesNulled(t *testing.T) {
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	ninf := float32(math.Inf(-1))
	cases := []struct {
		name         string
		brake, thr   float32
		bNull, tNull bool
		invalid      int
	}{
		{"nan brake", nan, 5, true, false, 1},
		{"+inf throttle", 5, inf, false, true, 1},
		{"-inf both", ninf, ninf, true, true, 2},
		{"neg brake", -0.0001, 5, true, false, 1},
		{"brake over", 100.0001, 5, true, false, 1},
		{"throttle neg", 5, -1, false, true, 1},
		{"throttle over", 5, 100.0001, false, true, 1},
		{"both out", 101, -1, true, true, 2},
		{"valid bounds", 0, 100, false, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, inv := mustProject(t, frame(1, c.brake, c.thr), 0)
			if inv != c.invalid {
				t.Errorf("invalid = %d, want %d", inv, c.invalid)
			}
			if (s.Inputs.Brake == nil) != c.bNull || (s.Inputs.Throttle == nil) != c.tNull {
				t.Errorf("nil mismatch: %+v", s.Inputs)
			}
			out := string(mustMarshal(t, s))
			if strings.Contains(out, "NaN") || strings.Contains(out, "Inf") {
				t.Errorf("bad token in %s", out)
			}
		})
	}
}

func TestZeroValuesAreNumbers(t *testing.T) {
	s, inv := mustProject(t, frame(1, 0, 0), 0)
	if inv != 0 || s.Inputs.Brake == nil || s.Inputs.Throttle == nil {
		t.Fatalf("zero must not be null: %+v inv=%d", s.Inputs, inv)
	}
	in := asAny(t, s)["inputs"].(map[string]any)
	if in["brake"] != float64(0) || in["throttle"] != float64(0) {
		t.Errorf("inputs = %v", in)
	}
}

func TestNegativeZeroNormalized(t *testing.T) {
	negz := float32(math.Copysign(0, -1))
	s, inv := mustProject(t, frame(1, negz, negz), 0)
	if inv != 0 {
		t.Fatalf("invalid = %d", inv)
	}
	out := string(mustMarshal(t, s))
	if strings.Contains(out, `"brake":-0`) || strings.Contains(out, `"throttle":-0`) {
		t.Errorf("negative zero leaked: %s", out)
	}
	if !strings.Contains(out, `"brake":0,"throttle":0`) {
		t.Errorf("want 0 numbers: %s", out)
	}
}

func TestOverlapPreserved(t *testing.T) {
	s, _ := mustProject(t, frame(1, 33.5, 77.25), 0)
	if *s.Inputs.Brake != 33.5 || *s.Inputs.Throttle != 77.25 {
		t.Errorf("overlap altered: %v %v", *s.Inputs.Brake, *s.Inputs.Throttle)
	}
}

func TestSteeringAlwaysNull(t *testing.T) {
	f := frame(1, 10, 20)
	f.WheelRotation = 3.14159
	f.SteeringAngularVelocity = 2.71828
	f.WheelSteeringAngleFrontLeft = 0.123457
	f.WheelSteeringAngleFrontRight = 0.234568
	s, _ := mustProject(t, f, 0)
	out := string(mustMarshal(t, s))
	if !strings.Contains(out, `"steering":null`) {
		t.Errorf("steering not null: %s", out)
	}
	for _, bad := range []string{"3.14159", "2.71828", "0.123457", "0.234568", "wheel", "rotation", "angular", "Angle"} {
		if strings.Contains(out, bad) {
			t.Errorf("output leaks %q: %s", bad, out)
		}
	}
	h := asAny(t, NewHello(fixSession, fixVersion, StateLive))
	st := h["capabilities"].(map[string]any)["steering"].(map[string]any)
	if st["available"] != false || st["reason"] != ReasonSteeringUnverified {
		t.Errorf("steering capability = %v", st)
	}
}

func TestSourceMapping(t *testing.T) {
	for _, c := range []struct {
		in   telemetryv1.Source
		want string
	}{
		{telemetryv1.Source_SOURCE_LIVE, "LIVE"},
		{telemetryv1.Source_SOURCE_REPLAY, "REPLAY"},
		{telemetryv1.Source_SOURCE_UNSPECIFIED, "UNSPECIFIED"},
		{telemetryv1.Source(99), "UNSPECIFIED"},
	} {
		f := frame(1, 1, 1)
		f.Source = c.in
		if s, _ := mustProject(t, f, 0); s.Source != c.want {
			t.Errorf("%v -> %q, want %q", c.in, s.Source, c.want)
		}
	}
}

func TestProjectErrors(t *testing.T) {
	noTS := frame(1, 1, 1)
	noTS.RecvTimestamp = nil
	badTS := frame(1, 1, 1)
	badTS.RecvTimestamp = &timestamppb.Timestamp{Seconds: math.MaxInt64}
	for name, f := range map[string]*telemetryv1.TelemetryFrame{"nil": nil, "no ts": noTS, "invalid ts": badTS} {
		if _, _, err := Project(fixSession, f, 0); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRealFixturesEndToEnd(t *testing.T) {
	rec := &recorder{}
	p := ingest.NewPipeline(nil, rec)
	paths, err := filepath.Glob(filepath.Join("..", "protocol", "testdata", "packet_0*.bin"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob: %v %d", err, len(paths))
	}
	for i, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p.Handle(b, time.Unix(int64(i), 0))
	}
	if len(rec.frames) != len(paths) {
		t.Fatalf("published %d of %d", len(rec.frames), len(paths))
	}
	var prev uint64
	for i, f := range rec.frames {
		s, inv := mustProject(t, f, 0)
		if inv != 0 {
			t.Errorf("frame %d: invalid %d", i, inv)
		}
		validate(t, s)
		if math.Float32bits(*s.Inputs.Brake) != math.Float32bits(f.Brake) ||
			math.Float32bits(*s.Inputs.Throttle) != math.Float32bits(f.Throttle) {
			t.Errorf("frame %d: inputs differ from canonical frame", i)
		}
		if s.Source != "LIVE" {
			t.Errorf("frame %d source %q", i, s.Source)
		}
		seq, err := strconv.ParseUint(s.Sequence, 10, 64)
		if err != nil || seq <= prev {
			t.Errorf("frame %d: sequence %q not increasing after %d", i, s.Sequence, prev)
		}
		prev = seq
	}
}

type recorder struct{ frames []*telemetryv1.TelemetryFrame }

func (r *recorder) Publish(f *telemetryv1.TelemetryFrame) { r.frames = append(r.frames, f) }

func TestTopLevelKeySets(t *testing.T) {
	s, _ := mustProject(t, frame(1, 1, 1), 0)
	sm := asAny(t, s)
	wantS := []string{"droppedSincePrevious", "inputs", "packageId", "protocol", "recvTimestamp", "sequence", "sessionId", "source", "sourceGap", "type"}
	if got := keys(sm); !reflect.DeepEqual(got, wantS) {
		t.Errorf("sample keys %v", got)
	}
	if got := keys(sm["inputs"].(map[string]any)); !reflect.DeepEqual(got, []string{"brake", "steering", "throttle"}) {
		t.Errorf("inputs keys %v", got)
	}
	h := asAny(t, NewHello(fixSession, fixVersion, StateLive))
	wantH := []string{"canonicalSchemaVersion", "capabilities", "collectorVersion", "protocol", "sessionId", "status", "type"}
	if got := keys(h); !reflect.DeepEqual(got, wantH) {
		t.Errorf("hello keys %v", got)
	}
	if got := keys(h["capabilities"].(map[string]any)); !reflect.DeepEqual(got, []string{"brake", "steering", "throttle"}) {
		t.Errorf("capabilities keys %v", got)
	}
	st := asAny(t, NewStatus(fixSession, StateLive, ReasonFirstFrame))
	if got := keys(st); !reflect.DeepEqual(got, []string{"protocol", "reason", "sessionId", "state", "type"}) {
		t.Errorf("status keys %v", got)
	}
	if h["type"] != "hello" || sm["type"] != "sample" || st["type"] != "status" ||
		h["protocol"] != Protocol || sm["protocol"] != Protocol || st["protocol"] != Protocol {
		t.Error("type/protocol wrong")
	}
	if h["canonicalSchemaVersion"] != float64(telemetry.SchemaVersion) {
		t.Error("canonicalSchemaVersion wrong")
	}
}

func TestDroppedSince(t *testing.T) {
	for _, c := range []struct {
		name      string
		prev, cur uint64
		have      bool
		want      uint64
	}{
		{"first", 0, 5, false, 0},
		{"contiguous", 5, 6, true, 0},
		{"gap of 7", 5, 13, true, 7},
		{"equal", 5, 5, true, 0},
		{"cur<prev", 9, 5, true, 0},
	} {
		if got := DroppedSince(c.prev, c.cur, c.have); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestNewSessionID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	a, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSessionID()
	if !re.MatchString(a) || !re.MatchString(b) || a == b {
		t.Errorf("bad ids %q %q", a, b)
	}
}

func wireEqual(t *testing.T, path string, dst any, built any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("unmarshal fixture into %T: %v", dst, err)
	}
	if got, want := mustMarshal(t, dst), mustMarshal(t, built); !bytes.Equal(got, want) {
		t.Errorf("wire bytes differ:\nfixture %s\nbuilt   %s", got, want)
	}
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile("testdata/v1/schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

func validateBytes(t *testing.T, sch *jsonschema.Schema, b []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return sch.Validate(inst)
}

func validate(t *testing.T, v any) {
	t.Helper()
	if err := validateBytes(t, compileSchema(t), mustMarshal(t, v)); err != nil {
		t.Errorf("encoder output fails schema: %v", err)
	}
}

func TestFixturesAgainstSchema(t *testing.T) {
	sch := compileSchema(t)
	for dir, wantValid := range map[string]bool{"valid": true, "invalid": false} {
		files, err := filepath.Glob(filepath.Join("testdata/v1", dir, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no fixtures in %s", dir)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateBytes(t, sch, b); (err == nil) != wantValid {
				t.Errorf("%s: valid=%v want %v (%v)", f, err == nil, wantValid, err)
			}
		}
	}
}

func TestWireBytes(t *testing.T) {
	v := float32(190) / 2.55
	s, _ := mustProject(t, frame(1, v, 255/2.55), 0)
	out := string(mustMarshal(t, s))
	for _, want := range []string{`"brake":74.5098`, `"throttle":100`, `"steering":null`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.ContainsAny(out, " \n\t") {
		t.Errorf("not compact: %s", out)
	}
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), `"recvTimestamp":"2026-11-14T12:00:00Z"`},
		{time.Date(2026, 11, 14, 12, 0, 0, 500_000_000, time.UTC), `"recvTimestamp":"2026-11-14T12:00:00.5Z"`},
		{time.Date(2026, 11, 14, 12, 0, 0, 1, time.UTC), `"recvTimestamp":"2026-11-14T12:00:00.000000001Z"`},
	} {
		f := frame(1, 1, 1)
		f.RecvTimestamp = timestamppb.New(c.t)
		s, _ := mustProject(t, f, 0)
		if out := string(mustMarshal(t, s)); !strings.Contains(out, c.want) {
			t.Errorf("want %s in %s", c.want, out)
		}
	}
}

func fieldNames(v any) []string {
	var n []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		n = append(n, rt.Field(i).Name)
	}
	sort.Strings(n)
	return n
}

func TestStructFieldsExact(t *testing.T) {
	for _, c := range []struct {
		v    any
		want []string
	}{
		{Sample{}, []string{"DroppedSincePrevious", "Inputs", "PackageID", "Protocol", "RecvTimestamp", "Sequence", "SessionID", "Source", "SourceGap", "Type"}},
		{Inputs{}, []string{"Brake", "Steering", "Throttle"}},
		{Hello{}, []string{"CanonicalSchemaVersion", "Capabilities", "CollectorVersion", "Protocol", "SessionID", "Status", "Type"}},
		{Status{}, []string{"Protocol", "Reason", "SessionID", "State", "Type"}},
	} {
		if got := fieldNames(c.v); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%T fields = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestFullyPopulatedFrameKeys(t *testing.T) {
	f := &telemetryv1.TelemetryFrame{}
	m := f.ProtoReflect()
	fds := m.Descriptor().Fields()
	for i := 0; i < fds.Len(); i++ {
		fd := fds.Get(i)
		if fd.IsList() || fd.IsMap() {
			continue
		}
		switch fd.Kind() {
		case protoreflect.BoolKind:
			m.Set(fd, protoreflect.ValueOfBool(true))
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			m.Set(fd, protoreflect.ValueOfInt32(7))
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			m.Set(fd, protoreflect.ValueOfInt64(7))
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			m.Set(fd, protoreflect.ValueOfUint32(7))
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			m.Set(fd, protoreflect.ValueOfUint64(7))
		case protoreflect.FloatKind:
			m.Set(fd, protoreflect.ValueOfFloat32(7.5))
		case protoreflect.DoubleKind:
			m.Set(fd, protoreflect.ValueOfFloat64(7.5))
		case protoreflect.StringKind:
			m.Set(fd, protoreflect.ValueOfString("x"))
		case protoreflect.EnumKind:
			m.Set(fd, protoreflect.ValueOfEnum(1))
		}
	}
	f.RecvTimestamp = timestamppb.New(fixTime)
	f.Brake, f.Throttle = 10, 20
	s, _ := mustProject(t, f, 0)
	sm := asAny(t, s)
	wantS := []string{"droppedSincePrevious", "inputs", "packageId", "protocol", "recvTimestamp", "sequence", "sessionId", "source", "sourceGap", "type"}
	if got := keys(sm); !reflect.DeepEqual(got, wantS) {
		t.Errorf("sample keys %v", got)
	}
	if got := keys(sm["inputs"].(map[string]any)); !reflect.DeepEqual(got, []string{"brake", "steering", "throttle"}) {
		t.Errorf("inputs keys %v", got)
	}
	if out := string(mustMarshal(t, s)); strings.Contains(out, "7.5") {
		t.Errorf("leaked field value: %s", out)
	}
}

func TestSteeringRejectsNonNull(t *testing.T) {
	var in Inputs
	if err := json.Unmarshal([]byte(`{"brake":1,"throttle":2,"steering":null}`), &in); err != nil {
		t.Fatalf("null steering: %v", err)
	}
	for _, bad := range []string{`0`, `1.5`, `"x"`, `{}`} {
		if err := json.Unmarshal([]byte(`{"steering":`+bad+`}`), &in); err == nil {
			t.Errorf("steering %s accepted", bad)
		}
	}
}
