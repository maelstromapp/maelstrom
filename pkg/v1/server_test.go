package v1

import (
	"fmt"
	"github.com/coopernurse/barrister-go"
	. "github.com/franela/goblin"
	"github.com/google/gofuzz"
	"gitlab.com/coopernurse/maelstrom/pkg/db"
	"testing"
)

func TestComponent(t *testing.T) {
	svc := NewV1(db.NewMemDb())
	f := fuzz.New()

	g := Goblin(t)
	g.Describe("Component CRUD", func() {
		g.It("Put saves a new component", func() {
			input := PutComponentInput{
				Name: "abc",
				Docker: DockerComponent{
					Image:    "coopernurse/foo",
					HttpPort: 8080,
				},
			}
			out, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()
			g.Assert(out.Name).Eql(input.Name)
			g.Assert(out.Version).Eql(int64(1))
		})
		g.It("Put updates an existing component with the same name", func() {
			input := PutComponentInput{
				Name: "c2",
				Docker: DockerComponent{
					Image:    "coopernurse/foo",
					HttpPort: 8081,
				},
			}
			_, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()

			expected := GetComponentOutput{
				Name:    input.Name,
				Version: 1,
				Docker:  input.Docker,
			}
			out, err := svc.GetComponent(GetComponentInput{Name: "c2"})
			g.Assert(err == nil).IsTrue()
			g.Assert(out).Eql(expected)
		})
		g.It("Put and Get are symmetric", func() {
			for i := 0; i < 10; i++ {
				var input PutComponentInput
				for !componentNameRE.MatchString(input.Name) {
					f.Fuzz(&input)
				}
				input.PreviousVersion = 0
				out, err := svc.PutComponent(input)
				g.Assert(err == nil).IsTrue(fmt.Sprintf("Got err: %v", err))

				expected := GetComponentOutput{
					Name:    out.Name,
					Version: 1,
					Docker:  input.Docker,
				}

				getIn := GetComponentInput{Name: out.Name}
				getOut, err := svc.GetComponent(getIn)
				g.Assert(err == nil).IsTrue()
				g.Assert(getOut).Eql(expected)
			}
		})
	})
	g.Describe("PutComponent Validation", func() {
		g.It("Raises 1001 if name is invalid", func() {
			invalid := []PutComponentInput{
				{},
				{Name: " "},
				{Name: "\t\n"},
				{Name: "hello)"},
			}
			for _, i := range invalid {
				out, err := svc.PutComponent(i)
				g.Assert(out).Eql(PutComponentOutput{})
				g.Assert(err != nil).IsTrue(fmt.Sprintf("input: %+v", i))
				rpcErr, ok := err.(*barrister.JsonRpcError)
				g.Assert(ok).IsTrue()
				g.Assert(rpcErr.Code).Eql(1001)
			}

			valid := []PutComponentInput{
				{Name: "aBc"},
				{Name: "92dak_-9s9"},
			}
			for _, i := range valid {
				_, err := svc.PutComponent(i)
				g.Assert(err == nil).IsTrue(fmt.Sprintf("input: %+v", i))
			}
		})
		g.It("Raises 1002 if name already exists and previousVersion is zero", func() {
			input := PutComponentInput{
				Name:   "1002test",
				Docker: DockerComponent{Image: "coopernurse/foo", HttpPort: 8080},
			}
			// first put should succeed
			_, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()

			// put again - should raise 1002
			_, err = svc.PutComponent(input)
			g.Assert(err != nil).IsTrue()
			rpcErr, ok := err.(*barrister.JsonRpcError)
			g.Assert(ok).IsTrue()
			g.Assert(rpcErr.Code).Eql(1002)
		})
		g.It("Raises 1004 if previousVersion is not current", func() {
			input := PutComponentInput{
				Name:   "1004test",
				Docker: DockerComponent{Image: "coopernurse/foo", HttpPort: 8080},
			}
			// first put should succeed
			out, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()
			g.Assert(out.Version).Eql(int64(1))

			// put again - should raise 1004
			input.PreviousVersion = out.Version + 1
			out, err = svc.PutComponent(input)
			g.Assert(err != nil).IsTrue(fmt.Sprintf("expected err, got: %+v", out))
			rpcErr, ok := err.(*barrister.JsonRpcError)
			g.Assert(ok).IsTrue()
			g.Assert(rpcErr.Code).Eql(1004)
		})
	})
	g.Describe("GetComponent Validation", func() {
		g.It("Raises 1003 if no component has that name", func() {
			input := GetComponentInput{
				Name: "notfound",
			}
			_, err := svc.GetComponent(input)
			g.Assert(err != nil).IsTrue()
			rpcErr, _ := err.(*barrister.JsonRpcError)
			g.Assert(rpcErr.Code).Eql(1003)
		})
	})
}
