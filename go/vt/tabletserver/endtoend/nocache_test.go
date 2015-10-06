// Copyright 2015, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package endtoend

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/youtube/vitess/go/mysql"
	mproto "github.com/youtube/vitess/go/mysql/proto"
	"github.com/youtube/vitess/go/sqltypes"
	"github.com/youtube/vitess/go/vt/tabletserver/endtoend/framework"

	pb "github.com/youtube/vitess/go/vt/proto/query"
)

func TestSimpleRead(t *testing.T) {
	vstart := framework.DebugVars()
	_, err := framework.NewDefaultClient().Execute("select * from vtocc_test where intval=1", nil)
	if err != nil {
		t.Error(err)
		return
	}
	vend := framework.DebugVars()
	if err := compareIntDiff(vend, "Queries/TotalCount", vstart, 1); err != nil {
		t.Error(err)
	}
	if err := compareIntDiff(vend, "Queries/Histograms/PASS_SELECT/Count", vstart, 1); err != nil {
		t.Error(err)
	}
}

func TestBinary(t *testing.T) {
	client := framework.NewDefaultClient()
	defer client.Execute("delete from vtocc_test where intval in (4,5)", nil)

	binaryData := "\x00'\"\b\n\r\t\x1a\\\x00\x0f\xf0\xff"
	// Test without bindvars.
	_, err := client.Execute(
		"insert into vtocc_test values "+
			"(4, null, null, '\\0\\'\\\"\\b\\n\\r\\t\\Z\\\\\x00\x0f\xf0\xff')",
		nil,
	)
	if err != nil {
		t.Error(err)
		return
	}
	qr, err := client.Execute("select binval from vtocc_test where intval=4", nil)
	if err != nil {
		t.Error(err)
		return
	}
	want := mproto.QueryResult{
		Fields: []mproto.Field{
			{
				Name:  "binval",
				Type:  mysql.TypeVarString,
				Flags: mysql.FlagBinary,
			},
		},
		RowsAffected: 1,
		Rows: [][]sqltypes.Value{
			[]sqltypes.Value{
				sqltypes.Value{Inner: sqltypes.String(binaryData)},
			},
		},
	}
	if !reflect.DeepEqual(*qr, want) {
		t.Errorf("Execute: \n%#v, want \n%#v", *qr, want)
	}

	// Test with bindvars.
	_, err = client.Execute(
		"insert into vtocc_test values(5, null, null, :bindata)",
		map[string]interface{}{"bindata": binaryData},
	)
	if err != nil {
		t.Error(err)
		return
	}
	qr, err = client.Execute("select binval from vtocc_test where intval=5", nil)
	if err != nil {
		t.Error(err)
		return
	}
	if !reflect.DeepEqual(*qr, want) {
		t.Errorf("Execute: \n%#v, want \n%#v", *qr, want)
	}
}

func TestNocacheListArgs(t *testing.T) {
	client := framework.NewDefaultClient()
	query := "select * from vtocc_test where intval in ::list"

	qr, err := client.Execute(
		query,
		map[string]interface{}{
			"list": []interface{}{2, 3, 4},
		},
	)
	if err != nil {
		t.Error(err)
		return
	}
	if qr.RowsAffected != 2 {
		t.Errorf("rows affected: %d, want 2", qr.RowsAffected)
	}

	qr, err = client.Execute(
		query,
		map[string]interface{}{
			"list": []interface{}{3, 4},
		},
	)
	if err != nil {
		t.Error(err)
		return
	}
	if qr.RowsAffected != 1 {
		t.Errorf("rows affected: %d, want 1", qr.RowsAffected)
	}

	qr, err = client.Execute(
		query,
		map[string]interface{}{
			"list": []interface{}{3},
		},
	)
	if err != nil {
		t.Error(err)
		return
	}
	if qr.RowsAffected != 1 {
		t.Errorf("rows affected: %d, want 1", qr.RowsAffected)
	}

	// Error case
	_, err = client.Execute(
		query,
		map[string]interface{}{
			"list": []interface{}{},
		},
	)
	want := "error: empty list supplied for list"
	if err == nil || err.Error() != want {
		t.Errorf("error returned: %v, want %s", err, want)
		return
	}
}

func TestIntegrityError(t *testing.T) {
	vstart := framework.DebugVars()
	client := framework.NewDefaultClient()
	_, err := client.Execute("insert into vtocc_test values(1, null, null, null)", nil)
	want := "error: Duplicate entry '1'"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Error: %v, want prefix %s", err, want)
	}
	if err := compareIntDiff(framework.DebugVars(), "InfoErrors/DupKey", vstart, 1); err != nil {
		t.Error(err)
	}
}

func TestTrailingComment(t *testing.T) {
	vstart := framework.DebugVars()
	v1 := framework.FetchInt(vstart, "QueryCacheLength")

	bindVars := map[string]interface{}{"ival": 1}
	client := framework.NewDefaultClient()

	for _, query := range []string{
		"select * from vtocc_test where intval=:ival",
		"select * from vtocc_test where intval=:ival /* comment */",
		"select * from vtocc_test where intval=:ival /* comment1 */ /* comment2 */",
	} {
		_, err := client.Execute(query, bindVars)
		if err != nil {
			t.Error(err)
			return
		}
		v2 := framework.FetchInt(framework.DebugVars(), "QueryCacheLength")
		if v2 != v1+1 {
			t.Errorf("QueryCacheLength(%s): %d, want %d", query, v2, v1+1)
		}
	}
}

func TestUpsertNonPKHit(t *testing.T) {
	client := framework.NewDefaultClient()
	err := client.Begin()
	if err != nil {
		t.Error(err)
		return
	}
	defer client.Rollback()

	_, err = client.Execute("insert into upsert_test(id1, id2) values (1, 1)", nil)
	if err != nil {
		t.Error(err)
		return
	}
	_, err = client.Execute(
		"insert into upsert_test(id1, id2) values "+
			"(2, 1) on duplicate key update id2 = 2",
		nil,
	)
	want := "error: Duplicate entry '1' for key 'id2_idx'"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Execute: %v, must start with %s", err, want)
	}
}

func TestSchemaReload(t *testing.T) {
	conn, err := mysql.Connect(connParams)
	if err != nil {
		t.Error(err)
		return
	}
	_, err = conn.ExecuteFetch("create table vtocc_temp(intval int)", 10, false)
	if err != nil {
		t.Error(err)
		return
	}
	defer func() {
		_, _ = conn.ExecuteFetch("drop table vtocc_temp", 10, false)
		conn.Close()
	}()
	framework.DefaultServer.ReloadSchema()
	client := framework.NewDefaultClient()
	waitTime := 50 * time.Millisecond
	for i := 0; i < 10; i++ {
		time.Sleep(waitTime)
		waitTime += 50 * time.Millisecond
		_, err = client.Execute("select * from vtocc_temp", nil)
		if err == nil {
			return
		}
		want := "error: table vtocc_temp not found in schema"
		if err.Error() != want {
			t.Errorf("Error: %v, want %s", err, want)
			return
		}
	}
	t.Error("schema did not reload")
}

func TestConsolidation(t *testing.T) {
	vstart := framework.DebugVars()
	defer framework.DefaultServer.SetPoolSize(framework.DefaultServer.PoolSize())
	framework.DefaultServer.SetPoolSize(1)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		framework.NewDefaultClient().Execute("select sleep(0.25) from dual", nil)
		wg.Done()
	}()
	go func() {
		framework.NewDefaultClient().Execute("select sleep(0.25) from dual", nil)
		wg.Done()
	}()
	wg.Wait()

	vend := framework.DebugVars()
	if err := compareIntDiff(vend, "Waits/TotalCount", vstart, 1); err != nil {
		t.Error(err)
	}
	if err := compareIntDiff(vend, "Waits/Histograms/Consolidations/Count", vstart, 1); err != nil {
		t.Error(err)
	}
}

func TestBindInSelect(t *testing.T) {
	client := framework.NewDefaultClient()

	// Int bind var.
	qr, err := client.Execute(
		"select :bv from dual",
		map[string]interface{}{"bv": 1},
	)
	if err != nil {
		t.Error(err)
		return
	}
	want := &mproto.QueryResult{
		Fields: []mproto.Field{{
			Name:  "1",
			Type:  mysql.TypeLonglong,
			Flags: mysql.FlagBinary,
		}},
		RowsAffected: 1,
		Rows: [][]sqltypes.Value{
			[]sqltypes.Value{
				sqltypes.Value{Inner: sqltypes.Numeric("1")},
			},
		},
	}
	if !reflect.DeepEqual(qr, want) {
		t.Errorf("Execute: \n%#v, want \n%#v", qr, want)
	}

	// String bind var.
	qr, err = client.Execute(
		"select :bv from dual",
		map[string]interface{}{"bv": "abcd"},
	)
	if err != nil {
		t.Error(err)
		return
	}
	want = &mproto.QueryResult{
		Fields: []mproto.Field{{
			Name:  "abcd",
			Type:  mysql.TypeVarString,
			Flags: 0,
		}},
		RowsAffected: 1,
		Rows: [][]sqltypes.Value{
			[]sqltypes.Value{
				sqltypes.Value{Inner: sqltypes.String("abcd")},
			},
		},
	}
	if !reflect.DeepEqual(qr, want) {
		t.Errorf("Execute: \n%#v, want \n%#v", qr, want)
	}

	// Binary bind var.
	qr, err = client.Execute(
		"select :bv from dual",
		map[string]interface{}{"bv": "\x00\xff"},
	)
	if err != nil {
		t.Error(err)
		return
	}
	want = &mproto.QueryResult{
		Fields: []mproto.Field{{
			Name:  "",
			Type:  mysql.TypeVarString,
			Flags: 0,
		}},
		RowsAffected: 1,
		Rows: [][]sqltypes.Value{
			[]sqltypes.Value{
				sqltypes.Value{Inner: sqltypes.String("\x00\xff")},
			},
		},
	}
	if !reflect.DeepEqual(qr, want) {
		t.Errorf("Execute: \n%#v, want \n%#v", qr, want)
	}
}

func TestHealth(t *testing.T) {
	response, err := http.Get(fmt.Sprintf("%s/debug/health", framework.ServerAddress))
	if err != nil {
		t.Error(err)
		return
	}
	defer response.Body.Close()
	result, err := ioutil.ReadAll(response.Body)
	if err != nil {
		t.Error(err)
		return
	}
	if string(result) != "ok" {
		t.Errorf("Health check: %s, want ok", result)
	}
}

func TestStreamHealth(t *testing.T) {
	ch := make(chan *pb.StreamHealthResponse, 10)
	id, _ := framework.DefaultServer.StreamHealthRegister(ch)
	defer framework.DefaultServer.StreamHealthUnregister(id)
	framework.DefaultServer.BroadcastHealth(0, nil)
	health := <-ch
	if !reflect.DeepEqual(*health.Target, framework.Target) {
		t.Errorf("Health: %+v, want %+v", *health.Target, framework.Target)
	}
}

func TestQueryStats(t *testing.T) {
	client := framework.NewDefaultClient()
	vstart := framework.DebugVars()

	start := time.Now()
	query := "select /* query_stats */ eid from vtocc_a where eid = :eid"
	bv := map[string]interface{}{"eid": 1}
	_, _ = client.Execute(query, bv)
	stat := framework.QueryStats()[query]
	duration := int(time.Now().Sub(start))
	if stat.Time <= 0 || stat.Time > duration {
		t.Errorf("stat.Time: %d, must be between 0 and %d", stat.Time, duration)
	}
	stat.Time = 0
	want := framework.QueryStat{
		Query:      query,
		Table:      "vtocc_a",
		Plan:       "PASS_SELECT",
		QueryCount: 1,
		RowCount:   2,
		ErrorCount: 0,
	}
	if stat != want {
		t.Errorf("stat: %+v, want %+v", stat, want)
	}

	query = "select /* query_stats */ eid from vtocc_a where dontexist(eid) = :eid"
	_, _ = client.Execute(query, bv)
	stat = framework.QueryStats()[query]
	stat.Time = 0
	want = framework.QueryStat{
		Query:      query,
		Table:      "vtocc_a",
		Plan:       "PASS_SELECT",
		QueryCount: 1,
		RowCount:   0,
		ErrorCount: 1,
	}
	if stat != want {
		t.Errorf("stat: %+v, want %+v", stat, want)
	}
	vend := framework.DebugVars()
	if err := compareIntDiff(vend, "QueryCounts/vtocc_a.PASS_SELECT", vstart, 2); err != nil {
		t.Error(err)
	}
	if err := compareIntDiff(vend, "QueryRowCounts/vtocc_a.PASS_SELECT", vstart, 2); err != nil {
		t.Error(err)
	}
	if err := compareIntDiff(vend, "QueryErrorCounts/vtocc_a.PASS_SELECT", vstart, 1); err != nil {
		t.Error(err)
	}
}

func TestDBAStatements(t *testing.T) {
	client := framework.NewDefaultClient()

	qr, err := client.Execute("show variables like 'version'", nil)
	if err != nil {
		t.Error(err)
		return
	}
	wantCol := sqltypes.Value{Inner: sqltypes.String("version")}
	if !reflect.DeepEqual(qr.Rows[0][0], wantCol) {
		t.Errorf("Execute: \n%#v, want \n%#v", qr.Rows[0][0], wantCol)
	}

	qr, err = client.Execute("describe vtocc_a", nil)
	if err != nil {
		t.Error(err)
		return
	}
	if qr.RowsAffected != 4 {
		t.Errorf("RowsAffected: %d, want 4", qr.RowsAffected)
	}

	qr, err = client.Execute("explain vtocc_a", nil)
	if err != nil {
		t.Error(err)
		return
	}
	if qr.RowsAffected != 4 {
		t.Errorf("RowsAffected: %d, want 4", qr.RowsAffected)
	}
}