package dynamodb

import (
	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	bolt "go.etcd.io/bbolt"
)

type exprIn struct {
	ExpressionAttributeNames  map[string]string
	ExpressionAttributeValues map[string]AV
}

func (e exprIn) ctx() (*exprCtx, error) {
	return newExprCtx(e.ExpressionAttributeNames, e.ExpressionAttributeValues)
}

// itemTable loads a table for an item operation.
func (s *Service) itemTable(name string) (*Table, error) {
	if name == "" {
		return nil, validation("1 validation error detected: Value null at 'tableName' failed to satisfy constraint: Member must not be null")
	}
	t, err := s.getTable(name)
	if err != nil {
		return nil, errNotFoundGeneric
	}
	return t, nil
}

func buildCondition(ctx *exprCtx, condExpr *string, expected map[string]legacyCond, condOp, onFail string) (condition, error) {
	var c condition
	switch onFail {
	case "", "NONE":
	case "ALL_OLD":
		c.returnOld = true
	default:
		return c, validation("1 validation error detected: Value '%s' at 'returnValuesOnConditionCheckFailure' failed to satisfy constraint: Member must satisfy enum value set: [ALL_OLD, NONE]", onFail)
	}
	if condExpr != nil && expected != nil {
		return c, mixErr([]string{"Expected"}, []string{"ConditionExpression"})
	}
	if condOp != "" && expected == nil {
		return c, validation("ConditionalOperator can only be used when Expected has two or more elements")
	}
	var err error
	if condExpr != nil {
		c.expr, err = parseCondition(*condExpr, "ConditionExpression", ctx)
	} else if expected != nil {
		c.expr, err = legacyExpected(expected, condOp)
	}
	return c, err
}

func validReturnValues(rv string, allowed ...string) error {
	if rv == "" {
		return nil
	}
	for _, a := range allowed {
		if rv == a {
			return nil
		}
	}
	return validation("Return values set to invalid value")
}

func withAttrs(res map[string]any, attrs Item) {
	if len(attrs) > 0 {
		res["Attributes"] = attrs
	}
}

func withCapacity(res map[string]any, c map[string]any) {
	if c != nil {
		res["ConsumedCapacity"] = c
	}
}

// ---- PutItem ----

func (s *Service) awsPutItem(q *awsapi.Req) (any, error) {
	var in struct {
		TableName                           string
		Item                                Item
		ConditionExpression                 *string
		Expected                            map[string]legacyCond
		ConditionalOperator                 string
		ReturnValues                        string
		ReturnConsumedCapacity              string
		ReturnItemCollectionMetrics         string
		ReturnValuesOnConditionCheckFailure string
		exprIn
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:PutItem", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if err := validReturnValues(in.ReturnValues, "NONE", "ALL_OLD"); err != nil {
		return nil, err
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	ctx, err := in.exprIn.ctx()
	if err != nil {
		return nil, err
	}
	cond, err := buildCondition(ctx, in.ConditionExpression, in.Expected, in.ConditionalOperator, in.ReturnValuesOnConditionCheckFailure)
	if err != nil {
		return nil, err
	}
	if err := ctx.finish(); err != nil {
		return nil, err
	}
	t, err := s.itemTable(in.TableName)
	if err != nil {
		return nil, err
	}
	if in.Item == nil {
		return nil, validation("1 validation error detected: Value null at 'item' failed to satisfy constraint: Member must not be null")
	}
	key, err := prepItem(t, in.Item)
	if err != nil {
		return nil, err
	}
	var old Item
	err = s.itemTx(in.TableName, func(t *Table, tb *bolt.Bucket) error {
		old, err = s.putTx(tb, t, key, in.Item, cond)
		return err
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	if in.ReturnValues == "ALL_OLD" {
		withAttrs(res, old)
	}
	size := itemSize(in.Item)
	if old != nil && itemSize(old) > size {
		size = itemSize(old)
	}
	withCapacity(res, consumed(in.ReturnConsumedCapacity, t.Name, capacityUnits(size, true, false), true, "", false))
	return res, nil
}

// ---- GetItem ----

// buildProjection parses ProjectionExpression or AttributesToGet.
func buildProjection(ctx *exprCtx, projExpr *string, attrs []string) (*projection, error) {
	if projExpr != nil && attrs != nil {
		return nil, mixErr([]string{"AttributesToGet"}, []string{"ProjectionExpression"})
	}
	if projExpr != nil {
		return parseProjection(*projExpr, ctx)
	}
	if attrs != nil {
		if len(attrs) == 0 {
			return nil, validation("1 validation error detected: Value '[]' at 'attributesToGet' failed to satisfy constraint: Member must have length greater than or equal to 1")
		}
		seen := map[string]bool{}
		for _, a := range attrs {
			if seen[a] {
				return nil, validation("One or more parameter values were invalid: Duplicate value in attribute name: %s", a)
			}
			seen[a] = true
		}
		return projectionOf(attrs), nil
	}
	return nil, nil
}

func (s *Service) awsGetItem(q *awsapi.Req) (any, error) {
	var in struct {
		TableName                string
		Key                      Item
		ConsistentRead           bool
		ProjectionExpression     *string
		AttributesToGet          []string
		ReturnConsumedCapacity   string
		ExpressionAttributeNames map[string]string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:GetItem", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	ctx, err := newExprCtx(in.ExpressionAttributeNames, nil)
	if err != nil {
		return nil, err
	}
	proj, err := buildProjection(ctx, in.ProjectionExpression, in.AttributesToGet)
	if err != nil {
		return nil, err
	}
	if err := ctx.finish(); err != nil {
		return nil, err
	}
	t, err := s.itemTable(in.TableName)
	if err != nil {
		return nil, err
	}
	key, err := t.schema().keyFrom(in.Key)
	if err != nil {
		return nil, err
	}
	it, err := s.getOne(in.TableName, key)
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	if it != nil {
		res["Item"] = proj.apply(it)
	}
	withCapacity(res, consumed(in.ReturnConsumedCapacity, t.Name, capacityUnits(itemSize(it), false, in.ConsistentRead), false, "", false))
	return res, nil
}

func (s *Service) getOne(table string, key []byte) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, err := s.itemTable(table)
	if err != nil {
		return nil, err
	}
	var it Item
	err = s.db.View(func(tx *bolt.Tx) error {
		tb, err := tableBucket(tx, t)
		if err != nil {
			return errNotFoundGeneric
		}
		it, err = getItem(tb, key)
		return err
	})
	return it, err
}

// ---- UpdateItem ----

func (s *Service) awsUpdateItem(q *awsapi.Req) (any, error) {
	var in struct {
		TableName                           string
		Key                                 Item
		UpdateExpression                    *string
		AttributeUpdates                    map[string]attributeUpdate
		ConditionExpression                 *string
		Expected                            map[string]legacyCond
		ConditionalOperator                 string
		ReturnValues                        string
		ReturnConsumedCapacity              string
		ReturnItemCollectionMetrics         string
		ReturnValuesOnConditionCheckFailure string
		exprIn
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:UpdateItem", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if err := validReturnValues(in.ReturnValues, "NONE", "ALL_OLD", "UPDATED_OLD", "ALL_NEW", "UPDATED_NEW"); err != nil {
		return nil, err
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	ctx, err := in.exprIn.ctx()
	if err != nil {
		return nil, err
	}
	if in.UpdateExpression != nil && in.AttributeUpdates != nil {
		return nil, mixErr([]string{"AttributeUpdates"}, []string{"UpdateExpression"})
	}
	if in.AttributeUpdates != nil && in.ConditionExpression != nil {
		return nil, mixErr([]string{"AttributeUpdates"}, []string{"ConditionExpression"})
	}
	if in.UpdateExpression != nil && in.Expected != nil {
		return nil, mixErr([]string{"Expected"}, []string{"UpdateExpression"})
	}
	var upd *updateExpr
	if in.UpdateExpression != nil {
		if upd, err = parseUpdate(*in.UpdateExpression, ctx); err != nil {
			return nil, err
		}
	} else if in.AttributeUpdates != nil {
		if upd, err = legacyUpdates(in.AttributeUpdates); err != nil {
			return nil, err
		}
	}
	cond, err := buildCondition(ctx, in.ConditionExpression, in.Expected, in.ConditionalOperator, in.ReturnValuesOnConditionCheckFailure)
	if err != nil {
		return nil, err
	}
	if err := ctx.finish(); err != nil {
		return nil, err
	}
	t, err := s.itemTable(in.TableName)
	if err != nil {
		return nil, err
	}
	key, err := t.schema().keyFrom(in.Key)
	if err != nil {
		return nil, err
	}
	var fn updater
	if upd != nil {
		// Reject key updates before touching storage, even when the condition fails.
		for _, a := range upd.actions {
			if t.schema().has(a.path[0].Name) {
				return nil, validation("One or more parameter values were invalid: Cannot update attribute %s. This attribute is part of the key", a.path[0].Name)
			}
		}
		fn = exprUpdater(upd, t.schema())
	}
	var old, nu Item
	err = s.itemTx(in.TableName, func(t *Table, tb *bolt.Bucket) error {
		old, nu, err = s.updateTx(tb, t, in.Key, key, fn, cond)
		return err
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	switch in.ReturnValues {
	case "ALL_OLD":
		withAttrs(res, old)
	case "ALL_NEW":
		withAttrs(res, nu)
	case "UPDATED_OLD", "UPDATED_NEW":
		if upd != nil {
			src := old
			if in.ReturnValues == "UPDATED_NEW" {
				src = nu
			}
			withAttrs(res, upd.updatedPaths().apply(orEmpty(src)))
		}
	}
	size := itemSize(nu)
	if itemSize(old) > size {
		size = itemSize(old)
	}
	withCapacity(res, consumed(in.ReturnConsumedCapacity, t.Name, capacityUnits(size, true, false), true, "", false))
	return res, nil
}

// ---- DeleteItem ----

func (s *Service) awsDeleteItem(q *awsapi.Req) (any, error) {
	var in struct {
		TableName                           string
		Key                                 Item
		ConditionExpression                 *string
		Expected                            map[string]legacyCond
		ConditionalOperator                 string
		ReturnValues                        string
		ReturnConsumedCapacity              string
		ReturnItemCollectionMetrics         string
		ReturnValuesOnConditionCheckFailure string
		exprIn
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DeleteItem", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if err := validReturnValues(in.ReturnValues, "NONE", "ALL_OLD"); err != nil {
		return nil, err
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	ctx, err := in.exprIn.ctx()
	if err != nil {
		return nil, err
	}
	cond, err := buildCondition(ctx, in.ConditionExpression, in.Expected, in.ConditionalOperator, in.ReturnValuesOnConditionCheckFailure)
	if err != nil {
		return nil, err
	}
	if err := ctx.finish(); err != nil {
		return nil, err
	}
	t, err := s.itemTable(in.TableName)
	if err != nil {
		return nil, err
	}
	key, err := t.schema().keyFrom(in.Key)
	if err != nil {
		return nil, err
	}
	var old Item
	err = s.itemTx(in.TableName, func(t *Table, tb *bolt.Bucket) error {
		old, err = s.deleteTx(tb, t, key, cond)
		return err
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	if in.ReturnValues == "ALL_OLD" {
		withAttrs(res, old)
	}
	withCapacity(res, consumed(in.ReturnConsumedCapacity, t.Name, capacityUnits(itemSize(old), true, false), true, "", false))
	return res, nil
}

// ---- Query and Scan ----

type readIn struct {
	TableName              string
	IndexName              string
	Select                 string
	AttributesToGet        []string
	Limit                  *int
	ConsistentRead         bool
	ConditionalOperator    string
	ExclusiveStartKey      Item
	ReturnConsumedCapacity string
	ProjectionExpression   *string
	FilterExpression       *string
	exprIn
	// Query
	KeyConditions          map[string]legacyCond
	QueryFilter            map[string]legacyCond
	ScanIndexForward       *bool
	KeyConditionExpression *string
	// Scan
	ScanFilter    map[string]legacyCond
	Segment       *int
	TotalSegments *int
}

func (s *Service) awsQuery(q *awsapi.Req) (any, error) {
	var in readIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.awsRead(q, &in, true)
}

func (s *Service) awsScan(q *awsapi.Req) (any, error) {
	var in readIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.awsRead(q, &in, false)
}

func (s *Service) awsRead(q *awsapi.Req, in *readIn, isQuery bool) (any, error) {
	action, res := "dynamodb:Scan", s.tableARN(in.TableName)
	if isQuery {
		action = "dynamodb:Query"
	}
	if in.IndexName != "" {
		res += "/index/" + in.IndexName
	}
	if err := q.Authorize(action, res); err != nil {
		return nil, err
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	t, err := s.itemTable(in.TableName)
	if err != nil {
		return nil, err
	}
	r := readReq{index: in.IndexName, forward: true}
	var ix *Index
	local := false
	ks := t.schema()
	if in.IndexName != "" {
		if ix, local = t.index(in.IndexName); ix == nil {
			return nil, validation("The table does not have the specified index: %s", in.IndexName)
		}
		ks = ix.schema()
		if in.ConsistentRead && !local {
			return nil, validation("Consistent reads are not supported on global secondary indexes")
		}
	}
	if in.Limit != nil {
		if *in.Limit < 1 {
			return nil, validation("1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1", *in.Limit)
		}
		r.limit = *in.Limit
	}
	ctx, err := in.exprIn.ctx()
	if err != nil {
		return nil, err
	}
	// Key condition.
	if isQuery {
		if in.ScanIndexForward != nil {
			r.forward = *in.ScanIndexForward
		}
		var kc keyCond
		switch {
		case in.KeyConditionExpression != nil && in.KeyConditions != nil:
			return nil, mixErr([]string{"KeyConditions"}, []string{"KeyConditionExpression"})
		case in.KeyConditionExpression != nil:
			kc, err = parseKeyCondition(*in.KeyConditionExpression, ctx, ks)
		case in.KeyConditions != nil:
			kc, err = legacyKeyConditions(in.KeyConditions, ks)
		default:
			return nil, validation("Either the KeyConditions or KeyConditionExpression parameter must be specified in the request.")
		}
		if err != nil {
			return nil, err
		}
		r.kc = &kc
	} else {
		if in.Segment != nil || in.TotalSegments != nil {
			if in.TotalSegments == nil {
				return nil, validation("The TotalSegments parameter is required but was not present in the request when Segment parameter is present")
			}
			if in.Segment == nil {
				return nil, validation("The Segment parameter is required but was not present in the request when parameter TotalSegments is present")
			}
			if *in.TotalSegments < 1 || *in.TotalSegments > 1000000 {
				return nil, validation("1 validation error detected: Value '%d' at 'totalSegments' failed to satisfy constraint: Member must have value less than or equal to 1000000", *in.TotalSegments)
			}
			if *in.Segment < 0 || *in.Segment >= *in.TotalSegments {
				return nil, validation("The Segment parameter is zero-based and must be less than parameter TotalSegments: Segment: %d is not less than TotalSegments: %d", *in.Segment, *in.TotalSegments)
			}
			r.segment, r.segments = *in.Segment, *in.TotalSegments
		}
	}
	// Filter.
	legacyFilter := in.QueryFilter
	legacyName := "QueryFilter"
	if !isQuery {
		legacyFilter, legacyName = in.ScanFilter, "ScanFilter"
	}
	switch {
	case in.FilterExpression != nil && legacyFilter != nil:
		return nil, mixErr([]string{legacyName}, []string{"FilterExpression"})
	case in.FilterExpression != nil:
		if r.filter, err = parseCondition(*in.FilterExpression, "FilterExpression", ctx); err != nil {
			return nil, err
		}
		if isQuery {
			attrs := map[string]bool{}
			condAttrs(r.filter, attrs)
			for _, n := range ks.names() {
				if attrs[n] {
					return nil, validation("Filter Expression can only contain non-primary key attributes: Primary key attribute: %s", n)
				}
			}
		}
	case legacyFilter != nil:
		if r.filter, err = legacyConditions(legacyFilter, in.ConditionalOperator); err != nil {
			return nil, err
		}
	}
	// Projection and Select.
	proj, err := buildProjection(ctx, in.ProjectionExpression, in.AttributesToGet)
	if err != nil {
		return nil, err
	}
	if err := ctx.finish(); err != nil {
		return nil, err
	}
	sel := in.Select
	switch sel {
	case "":
		if proj != nil {
			sel = "SPECIFIC_ATTRIBUTES"
		} else if ix != nil {
			sel = "ALL_PROJECTED_ATTRIBUTES"
		} else {
			sel = "ALL_ATTRIBUTES"
		}
	case "ALL_ATTRIBUTES", "ALL_PROJECTED_ATTRIBUTES", "COUNT", "SPECIFIC_ATTRIBUTES":
	default:
		return nil, validation("1 validation error detected: Value '%s' at 'select' failed to satisfy constraint: Member must satisfy enum value set: [SPECIFIC_ATTRIBUTES, COUNT, ALL_ATTRIBUTES, ALL_PROJECTED_ATTRIBUTES]", sel)
	}
	if proj != nil && sel != "SPECIFIC_ATTRIBUTES" {
		return nil, validation("Cannot specify the AttributesToGet or ProjectionExpression when choosing to get %s", sel)
	}
	if sel == "SPECIFIC_ATTRIBUTES" && proj == nil {
		return nil, validation("Select type SPECIFIC_ATTRIBUTES requires a ProjectionExpression or AttributesToGet")
	}
	if sel == "ALL_PROJECTED_ATTRIBUTES" && ix == nil {
		return nil, validation("ALL_PROJECTED_ATTRIBUTES can be used only when Querying using an IndexName")
	}
	if sel == "ALL_ATTRIBUTES" && ix != nil && ix.projection().Type != "ALL" {
		if !local {
			return nil, validation("One or more parameter values were invalid: Select type ALL_ATTRIBUTES is not supported for global secondary index %s because its projection type is not ALL", ix.Name)
		}
		r.allAttrs = true
	}
	if sel == "SPECIFIC_ATTRIBUTES" && local {
		r.allAttrs = true
	}
	r.countOnly = sel == "COUNT"
	r.proj = proj
	r.start = in.ExclusiveStartKey
	rr, err := s.read(in.TableName, r)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"Count": rr.count, "ScannedCount": rr.scanned}
	if !r.countOnly {
		items := rr.items
		if items == nil {
			items = []Item{}
		}
		out["Items"] = items
	}
	if rr.last != nil {
		out["LastEvaluatedKey"] = rr.last
	}
	withCapacity(out, consumed(in.ReturnConsumedCapacity, t.Name, capacityUnits(rr.bytes, false, in.ConsistentRead), false, in.IndexName, local))
	return out, nil
}
