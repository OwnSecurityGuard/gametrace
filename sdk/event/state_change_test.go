package event

import "testing"

// TestStateChangeValidateOpAfter 覆盖 op 与 after 的对应契约：
// set → after 必填任意非空值；merge → after 必填且必须是 Object；delete → after 不需要。
func TestStateChangeValidateOpAfter(t *testing.T) {
	obj := ValueObject(map[string]Value{"atk": ValueInt(1)})
	cases := []struct {
		name    string
		sc      StateChange
		wantErr bool
	}{
		{
			name: "set 带标量 after",
			sc:   StateChange{SubjectType: "P", SubjectID: "1", Op: "set", Path: "hp", After: ValueInt(100)},
		},
		{
			name:    "set 缺 after",
			sc:      StateChange{SubjectType: "P", SubjectID: "1", Op: "set", Path: "hp"},
			wantErr: true,
		},
		{
			name:    "set after 为 null",
			sc:      StateChange{SubjectType: "P", SubjectID: "1", Op: "set", Path: "hp", After: ValueNull()},
			wantErr: true,
		},
		{
			name: "merge 带对象片段",
			sc:   StateChange{SubjectType: "P", SubjectID: "1", Op: "merge", Path: "stats", After: obj},
		},
		{
			name:    "merge 缺 after",
			sc:      StateChange{SubjectType: "P", SubjectID: "1", Op: "merge", Path: "stats"},
			wantErr: true,
		},
		{
			name:    "merge after 非对象",
			sc:      StateChange{SubjectType: "P", SubjectID: "1", Op: "merge", Path: "stats", After: ValueString("x")},
			wantErr: true,
		},
		{
			name: "delete 不带 after",
			sc:   StateChange{SubjectType: "P", SubjectID: "1", Op: "delete", Path: "hp"},
		},
		{
			name: "delete 带 after 也放行（宿主侧忽略）",
			sc:   StateChange{SubjectType: "P", SubjectID: "1", Op: "delete", Path: "hp", After: ValueInt(1)},
		},
		{
			name:    "未知 op",
			sc:      StateChange{SubjectType: "P", SubjectID: "1", Op: "patch", Path: "hp", After: ValueInt(1)},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.sc.Validate(); (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}
