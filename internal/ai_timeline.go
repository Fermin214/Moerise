package moerise

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"time"
)

type AITimeRange struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	Until string `json:"until"`
}

type AITimelinePeriod struct {
	ID                  string         `json:"id"`
	From                string         `json:"from"`
	Until               string         `json:"until"`
	RepresentativeNames []string       `json:"representative_names"`
	StatementIndexes    []int          `json:"statement_indexes"`
	Empty               bool           `json:"empty"`
	MissingReason       string         `json:"missing_reason,omitempty"`
	Semantic            SearchSemantic `json:"semantic"`
}

func (s *AIService) timelineRanges(ctx context.Context, request AIRequest) ([]AITimeRange, error) {
	options, err := normalizeEvidence(request)
	if err != nil {
		return nil, err
	}
	if err := s.Search.verify(ctx); err != nil {
		return nil, err
	}
	where, args := searchFilter(s.Search.Store.Settings, options)
	var first, last sql.NullString
	if err := s.Search.Store.index.QueryRowContext(ctx, `SELECT
		min(json_extract(m.payload_json, '$.create_time')), max(json_extract(m.payload_json, '$.create_time'))
		FROM memos m WHERE `+where, args...).Scan(&first, &last); err != nil {
		return nil, err
	}
	if (!first.Valid || !last.Valid) && (options.createdFrom == nil || options.createdUntil == nil) {
		return []AITimeRange{}, nil
	}
	from, until := time.Time{}, time.Time{}
	if options.createdFrom != nil {
		from = *options.createdFrom
	} else {
		created, err := time.Parse(time.RFC3339Nano, first.String)
		if err != nil {
			return nil, upstreamError("UPSTREAM_PROTOCOL", "Invalid note creation time")
		}
		from = time.Date(created.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if options.createdUntil != nil {
		until = *options.createdUntil
	} else {
		created, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			return nil, upstreamError("UPSTREAM_PROTOCOL", "Invalid note creation time")
		}
		if created.Year() == 9999 {
			until = time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
		} else {
			until = time.Date(created.Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
	}
	if !from.Before(until) {
		return []AITimeRange{}, nil
	}
	from, until = from.UTC(), until.UTC()
	bounds := []time.Time{from}
	years := until.Year() - from.Year()
	if years >= 1 && years <= 5 && until.Sub(from) >= 365*24*time.Hour {
		for year := from.Year() + 1; year <= until.Year(); year++ {
			next := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
			if next.Before(until) {
				bounds = append(bounds, next)
			}
		}
	} else if until.Sub(from) >= 4*24*time.Hour {
		// Milliseconds stay within int64 across RFC 3339 years, unlike Duration.
		for i := int64(1); i < 4; i++ {
			bounds = append(bounds, time.UnixMilli(from.UnixMilli()+(until.UnixMilli()-from.UnixMilli())*i/4))
		}
	}
	bounds = append(bounds, until)
	periods := make([]AITimeRange, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		periods = append(periods, AITimeRange{ID: "P" + strconv.Itoa(i+1),
			From: bounds[i].Format(time.RFC3339Nano), Until: bounds[i+1].Format(time.RFC3339Nano)})
	}
	return periods, nil
}

func (s *EvidenceService) collectTimeline(ctx context.Context, request AIRequest, ranges []AITimeRange) (EvidenceSet, []AITimelinePeriod, error) {
	options, err := normalizeEvidence(request)
	if err != nil {
		return EvidenceSet{}, nil, err
	}
	result := EvidenceSet{Items: []Evidence{}, Omitted: []string{}, Semantic: SearchSemantic{Reason: "NO_EVIDENCE"}}
	periods := []AITimelinePeriod{}
	if len(ranges) == 0 {
		return result, periods, s.Verify(ctx, result.Items)
	}
	// Global IDs can be longer than each period's local IDs after joining.
	budget := (s.Budget - 128) / len(ranges)
	if budget < 2 {
		return EvidenceSet{}, nil, failure("EVIDENCE_BUDGET_EXCEEDED", "Timeline periods exceed the evidence budget", 422, false)
	}
	for _, span := range ranges {
		from, fromErr := time.Parse(time.RFC3339Nano, span.From)
		until, untilErr := time.Parse(time.RFC3339Nano, span.Until)
		if fromErr != nil || untilErr != nil || !from.Before(until) {
			return EvidenceSet{}, nil, failure("INVALID_ARGUMENT", "Invalid timeline period", 422, false)
		}
		options.createdFrom, options.createdUntil, options.Limit = &from, &until, 3
		found, err := s.Search.Search(ctx, options)
		if err != nil {
			return EvidenceSet{}, nil, err
		}
		if len(periods) == 0 || found.Semantic.Used {
			result.Semantic = found.Semantic
		}
		fitted := EvidenceSet{Items: []Evidence{}, Omitted: []string{}}
		part := NewEvidenceService(s.Search, budget)
		if err := part.fitBudget(&fitted, evidenceFromHits(found.Results), false); err != nil {
			return EvidenceSet{}, nil, err
		}
		period := AITimelinePeriod{ID: span.ID, From: span.From, Until: span.Until, Empty: len(fitted.Items) == 0,
			RepresentativeNames: []string{}, StatementIndexes: []int{}, Semantic: found.Semantic}
		if period.Empty {
			period.MissingReason = "NO_MATCHING_EVIDENCE"
			if len(found.Results) > 0 {
				period.MissingReason = "CONTEXT_BUDGET"
			}
		}
		for _, item := range fitted.Items {
			item.ID = "E" + strconv.Itoa(len(result.Items)+1)
			result.Items = append(result.Items, item)
			if !slices.Contains(period.RepresentativeNames, item.Name) {
				period.RepresentativeNames = append(period.RepresentativeNames, item.Name)
			}
		}
		for _, name := range fitted.Omitted {
			if !slices.Contains(result.Omitted, name) {
				result.Omitted = append(result.Omitted, name)
			}
		}
		periods = append(periods, period)
	}
	return result, periods, s.Verify(ctx, result.Items)
}

func assignTimelineStatements(result *AIResult) {
	for index, statement := range result.Statements {
		for p := range result.Timeline {
			period := &result.Timeline[p]
			for _, reference := range result.References {
				if slices.Contains(statement.Citations, reference.ID) &&
					slices.Contains(period.RepresentativeNames, reference.Name) {
					period.StatementIndexes = append(period.StatementIndexes, index)
					break
				}
			}
		}
	}
}
