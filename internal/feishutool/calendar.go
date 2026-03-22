package feishutool

import (
	"context"
	"fmt"
	"strconv"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkcalendar "github.com/larksuite/oapi-sdk-go/v3/service/calendar/v4"
	"github.com/vaayne/anna/internal/toolspec"
)

var calendarInputSchema = mustParseSchema(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create_event", "list_events", "get_event", "update_event", "delete_event", "add_attendees", "freebusy"],
      "description": "The action to perform"
    },
    "calendar_id": {
      "type": "string",
      "description": "Calendar ID. If omitted, the bot's primary calendar is used."
    },
    "event_id": {
      "type": "string",
      "description": "Event ID (required for get/update/delete/add_attendees)"
    },
    "summary": {
      "type": "string",
      "description": "Event title (required for create_event)"
    },
    "description": {
      "type": "string",
      "description": "Event description"
    },
    "start_time": {
      "type": "string",
      "description": "Start time in ISO 8601 format, e.g. '2024-01-01T09:00:00+08:00' (required for create_event, list_events, freebusy)"
    },
    "end_time": {
      "type": "string",
      "description": "End time in ISO 8601 format (required for create_event, list_events, freebusy)"
    },
    "visibility": {
      "type": "string",
      "enum": ["default", "public", "private"],
      "description": "Event visibility (default: 'default')"
    },
    "free_busy_status": {
      "type": "string",
      "enum": ["busy", "free"],
      "description": "Busy/free status (default: 'busy')"
    },
    "attendee_ability": {
      "type": "string",
      "enum": ["none", "can_see_others", "can_invite_others", "can_modify_event"],
      "description": "Attendee permission level"
    },
    "location": {
      "type": "object",
      "properties": {
        "name": {"type": "string"},
        "address": {"type": "string"}
      },
      "description": "Event location"
    },
    "reminders": {
      "type": "array",
      "items": {"type": "object", "properties": {"minutes": {"type": "number"}}},
      "description": "Reminder list. minutes > 0 = before event, minutes < 0 = after start"
    },
    "recurrence": {
      "type": "string",
      "description": "RRULE recurrence rule (RFC 5545), e.g. 'FREQ=DAILY;INTERVAL=1'"
    },
    "attendees": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string", "enum": ["user", "chat", "resource", "third_party"]},
          "id": {"type": "string", "description": "open_id, chat_id, resource_id, or email"}
        }
      },
      "description": "Attendees to add. type='user' uses open_id, type='third_party' uses email."
    },
    "user_open_id": {
      "type": "string",
      "description": "Current user's open_id (from context). For create_event, automatically added as attendee so the event appears in user's calendar."
    },
    "user_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "User open_ids to check availability (for freebusy action)"
    },
    "need_notification": {
      "type": "boolean",
      "description": "Whether to notify attendees on delete (default: true)"
    },
    "page_size": {
      "type": "number",
      "description": "Page size for list operations"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token for list operations"
    }
  },
  "required": ["action"]
}`)

// CalendarTool provides Feishu calendar event management.
type CalendarTool struct {
	client *Client
}

// NewCalendarTool creates a feishu_calendar tool.
func NewCalendarTool(client *Client) *CalendarTool {
	return &CalendarTool{client: client}
}

func (t *CalendarTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_calendar",
		Description: `Manage Feishu/Lark calendar events. Uses user token when available.

Actions:
- create_event: Create a calendar event. Requires summary, start_time, end_time. Pass user_open_id to add the user as attendee (otherwise the event only appears on the bot's calendar).
- list_events: List events in a time range (uses instance_view, auto-expands recurring events). Requires start_time, end_time. Time range must be < 40 days.
- get_event: Get event details by event_id.
- update_event: Update event fields (summary, description, start_time, end_time, location). Requires event_id.
- delete_event: Delete an event by event_id. Set need_notification=false to suppress notifications.
- add_attendees: Add attendees to an existing event. Requires event_id and attendees array.
- freebusy: Check user availability. Requires start_time, end_time, and user_ids array.

Time format: ISO 8601 with timezone, e.g. '2024-01-01T09:00:00+08:00'. Timestamps use Unix seconds internally.`,
		InputSchema: calendarInputSchema,
	}
}

func (t *CalendarTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "create_event":
		return t.createEvent(ctx, args)
	case "list_events":
		return t.listEvents(ctx, args)
	case "get_event":
		return t.getEvent(ctx, args)
	case "update_event":
		return t.updateEvent(ctx, args)
	case "delete_event":
		return t.deleteEvent(ctx, args)
	case "add_attendees":
		return t.addAttendees(ctx, args)
	case "freebusy":
		return t.freeBusy(ctx, args)
	default:
		return "", fmt.Errorf("feishu_calendar: unknown action %q", action)
	}
}

func (t *CalendarTool) resolveCalendarID(ctx context.Context, args map[string]any, opts ...larkcore.RequestOptionFunc) (string, error) {
	if id := stringArg(args, "calendar_id"); id != "" {
		return id, nil
	}
	// Resolve primary calendar.
	resp, err := t.client.Lark().Calendar.Calendar.Primary(ctx,
		larkcalendar.NewPrimaryCalendarReqBuilder().Build(),
		opts...)
	if err != nil {
		return "", fmt.Errorf("resolve primary calendar: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("resolve primary calendar: %s", FormatLarkError(resp.Code, resp.Msg))
	}
	if resp.Data != nil && resp.Data.Calendars != nil {
		for _, c := range resp.Data.Calendars {
			if c.Calendar != nil && c.Calendar.CalendarId != nil {
				return *c.Calendar.CalendarId, nil
			}
		}
	}
	return "", fmt.Errorf("could not determine primary calendar")
}

func (t *CalendarTool) createEvent(ctx context.Context, args map[string]any) (string, error) {
	summary := stringArg(args, "summary")
	if summary == "" {
		return "", fmt.Errorf("feishu_calendar create_event: summary is required")
	}
	startStr := stringArg(args, "start_time")
	endStr := stringArg(args, "end_time")
	if startStr == "" || endStr == "" {
		return "", fmt.Errorf("feishu_calendar create_event: start_time and end_time are required")
	}
	startUnix, err := ParseTimeToUnix(startStr)
	if err != nil {
		return "", fmt.Errorf("feishu_calendar create_event: invalid start_time: %w", err)
	}
	endUnix, err := ParseTimeToUnix(endStr)
	if err != nil {
		return "", fmt.Errorf("feishu_calendar create_event: invalid end_time: %w", err)
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		calID, err := t.resolveCalendarID(ctx, args, opts...)
		if err != nil {
			return err
		}

		event := buildCalendarEvent(args, summary, startUnix, endUnix)
		resp, err := t.client.Lark().Calendar.CalendarEvent.Create(ctx,
			larkcalendar.NewCreateCalendarEventReqBuilder().
				CalendarId(calID).
				CalendarEvent(event).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create event: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create event: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		result = map[string]any{"event": resp.Data.Event}

		// Add attendees (including user_open_id from args or context).
		if resp.Data.Event != nil && resp.Data.Event.EventId != nil {
			eventID := *resp.Data.Event.EventId
			attendees := buildAttendeeList(ctx, args)
			if len(attendees) > 0 {
				attErr := t.addAttendeesToEvent(ctx, calID, eventID, attendees, opts...)
				if attErr != nil {
					result["attendee_warning"] = attErr.Error()
				} else {
					result["attendees_added"] = len(attendees)
				}
			}
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar create_event: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *CalendarTool) listEvents(ctx context.Context, args map[string]any) (string, error) {
	startStr := stringArg(args, "start_time")
	endStr := stringArg(args, "end_time")
	if startStr == "" || endStr == "" {
		return "", fmt.Errorf("feishu_calendar list_events: start_time and end_time are required")
	}
	startUnix, err := ParseTimeToUnix(startStr)
	if err != nil {
		return "", fmt.Errorf("feishu_calendar list_events: invalid start_time: %w", err)
	}
	endUnix, err := ParseTimeToUnix(endStr)
	if err != nil {
		return "", fmt.Errorf("feishu_calendar list_events: invalid end_time: %w", err)
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		calID, err := t.resolveCalendarID(ctx, args, opts...)
		if err != nil {
			return err
		}

		resp, err := t.client.Lark().Calendar.CalendarEvent.InstanceView(ctx,
			larkcalendar.NewInstanceViewCalendarEventReqBuilder().
				CalendarId(calID).
				StartTime(strconv.FormatInt(startUnix, 10)).
				EndTime(strconv.FormatInt(endUnix, 10)).
				UserIdType("open_id").
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("list events: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list events: %s", FormatLarkError(resp.Code, resp.Msg))
		}

		result = map[string]any{
			"events":   resp.Data.Items,
			"has_more": false,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar list_events: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *CalendarTool) getEvent(ctx context.Context, args map[string]any) (string, error) {
	eventID := stringArg(args, "event_id")
	if eventID == "" {
		return "", fmt.Errorf("feishu_calendar get_event: event_id is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		calID, err := t.resolveCalendarID(ctx, args, opts...)
		if err != nil {
			return err
		}

		resp, err := t.client.Lark().Calendar.CalendarEvent.Get(ctx,
			larkcalendar.NewGetCalendarEventReqBuilder().
				CalendarId(calID).
				EventId(eventID).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get event: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get event: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"event": resp.Data.Event}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar get_event: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *CalendarTool) updateEvent(ctx context.Context, args map[string]any) (string, error) {
	eventID := stringArg(args, "event_id")
	if eventID == "" {
		return "", fmt.Errorf("feishu_calendar update_event: event_id is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		calID, err := t.resolveCalendarID(ctx, args, opts...)
		if err != nil {
			return err
		}

		event := buildCalendarEventPatch(args)
		resp, err := t.client.Lark().Calendar.CalendarEvent.Patch(ctx,
			larkcalendar.NewPatchCalendarEventReqBuilder().
				CalendarId(calID).
				EventId(eventID).
				CalendarEvent(event).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("update event: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("update event: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"event": resp.Data.Event}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar update_event: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *CalendarTool) deleteEvent(ctx context.Context, args map[string]any) (string, error) {
	eventID := stringArg(args, "event_id")
	if eventID == "" {
		return "", fmt.Errorf("feishu_calendar delete_event: event_id is required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		calID, err := t.resolveCalendarID(ctx, args, opts...)
		if err != nil {
			return err
		}

		builder := larkcalendar.NewDeleteCalendarEventReqBuilder().
			CalendarId(calID).
			EventId(eventID)

		if v, ok := boolArg(args, "need_notification"); ok {
			if v {
				builder.NeedNotification("true")
			} else {
				builder.NeedNotification("false")
			}
		}

		resp, err := t.client.Lark().Calendar.CalendarEvent.Delete(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("delete event: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("delete event: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar delete_event: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true, "event_id": eventID})
}

func (t *CalendarTool) addAttendees(ctx context.Context, args map[string]any) (string, error) {
	eventID := stringArg(args, "event_id")
	if eventID == "" {
		return "", fmt.Errorf("feishu_calendar add_attendees: event_id is required")
	}
	attendees := buildAttendeeList(ctx, args)
	if len(attendees) == 0 {
		return "", fmt.Errorf("feishu_calendar add_attendees: attendees is required")
	}

	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		calID, err := t.resolveCalendarID(ctx, args, opts...)
		if err != nil {
			return err
		}
		return t.addAttendeesToEvent(ctx, calID, eventID, attendees, opts...)
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar add_attendees: %w", invokeErr)
	}
	return JSONResultFromAny(map[string]any{"success": true, "attendees_added": len(attendees)})
}

func (t *CalendarTool) freeBusy(ctx context.Context, args map[string]any) (string, error) {
	startStr := stringArg(args, "start_time")
	endStr := stringArg(args, "end_time")
	if startStr == "" || endStr == "" {
		return "", fmt.Errorf("feishu_calendar freebusy: start_time and end_time are required")
	}
	startUnix, err := ParseTimeToUnix(startStr)
	if err != nil {
		return "", fmt.Errorf("feishu_calendar freebusy: invalid start_time: %w", err)
	}
	endUnix, err := ParseTimeToUnix(endStr)
	if err != nil {
		return "", fmt.Errorf("feishu_calendar freebusy: invalid end_time: %w", err)
	}
	userIDs := toStringSlice(args, "user_ids")
	if len(userIDs) == 0 {
		return "", fmt.Errorf("feishu_calendar freebusy: user_ids is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		body := larkcalendar.NewBatchFreebusyReqBodyBuilder().
			TimeMin(strconv.FormatInt(startUnix, 10)).
			TimeMax(strconv.FormatInt(endUnix, 10)).
			UserIds(userIDs).
			Build()

		resp, err := t.client.Lark().Calendar.Freebusy.Batch(ctx,
			larkcalendar.NewBatchFreebusyReqBuilder().
				UserIdType("open_id").
				Body(body).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("freebusy: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("freebusy: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = resp.Data
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_calendar freebusy: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

// addAttendeesToEvent adds attendees to an existing calendar event.
func (t *CalendarTool) addAttendeesToEvent(ctx context.Context, calID, eventID string, attendees []*larkcalendar.CalendarEventAttendee, opts ...larkcore.RequestOptionFunc) error {
	resp, err := t.client.Lark().Calendar.CalendarEventAttendee.Create(ctx,
		larkcalendar.NewCreateCalendarEventAttendeeReqBuilder().
			CalendarId(calID).
			EventId(eventID).
			UserIdType("open_id").
			Body(larkcalendar.NewCreateCalendarEventAttendeeReqBodyBuilder().
				Attendees(attendees).
				NeedNotification(true).
				Build()).
			Build(),
		opts...)
	if err != nil {
		return fmt.Errorf("add attendees: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("add attendees: %s", FormatLarkError(resp.Code, resp.Msg))
	}
	return nil
}

// buildCalendarEvent constructs a CalendarEvent for creation.
func buildCalendarEvent(args map[string]any, summary string, startUnix, endUnix int64) *larkcalendar.CalendarEvent {
	startTs := strconv.FormatInt(startUnix, 10)
	endTs := strconv.FormatInt(endUnix, 10)

	builder := larkcalendar.NewCalendarEventBuilder().
		Summary(summary).
		StartTime(larkcalendar.NewTimeInfoBuilder().Timestamp(startTs).Build()).
		EndTime(larkcalendar.NewTimeInfoBuilder().Timestamp(endTs).Build()).
		NeedNotification(true)

	if v := stringArg(args, "description"); v != "" {
		builder.Description(v)
	}
	if v := stringArg(args, "visibility"); v != "" {
		builder.Visibility(v)
	}
	if v := stringArg(args, "attendee_ability"); v != "" {
		builder.AttendeeAbility(v)
	}
	if v := stringArg(args, "free_busy_status"); v != "" {
		builder.FreeBusyStatus(v)
	}
	if v := stringArg(args, "recurrence"); v != "" {
		builder.Recurrence(v)
	}
	if loc := mapArg(args, "location"); loc != nil {
		locBuilder := larkcalendar.NewEventLocationBuilder()
		if name, ok := loc["name"].(string); ok {
			locBuilder.Name(name)
		}
		if addr, ok := loc["address"].(string); ok {
			locBuilder.Address(addr)
		}
		builder.Location(locBuilder.Build())
	}
	if rems := sliceArg(args, "reminders"); len(rems) > 0 {
		var reminders []*larkcalendar.Reminder
		for _, r := range rems {
			if rm, ok := r.(map[string]any); ok {
				if mins, ok := rm["minutes"].(float64); ok {
					reminders = append(reminders, larkcalendar.NewReminderBuilder().Minutes(int(mins)).Build())
				}
			}
		}
		if len(reminders) > 0 {
			builder.Reminders(reminders)
		}
	}

	return builder.Build()
}

// buildCalendarEventPatch constructs a CalendarEvent for patching (update).
func buildCalendarEventPatch(args map[string]any) *larkcalendar.CalendarEvent {
	builder := larkcalendar.NewCalendarEventBuilder()

	if v := stringArg(args, "summary"); v != "" {
		builder.Summary(v)
	}
	if v := stringArg(args, "description"); v != "" {
		builder.Description(v)
	}
	if v := stringArg(args, "start_time"); v != "" {
		if ts, err := ParseTimeToUnix(v); err == nil {
			builder.StartTime(larkcalendar.NewTimeInfoBuilder().Timestamp(strconv.FormatInt(ts, 10)).Build())
		}
	}
	if v := stringArg(args, "end_time"); v != "" {
		if ts, err := ParseTimeToUnix(v); err == nil {
			builder.EndTime(larkcalendar.NewTimeInfoBuilder().Timestamp(strconv.FormatInt(ts, 10)).Build())
		}
	}
	if loc := mapArg(args, "location"); loc != nil {
		locBuilder := larkcalendar.NewEventLocationBuilder()
		if name, ok := loc["name"].(string); ok {
			locBuilder.Name(name)
		}
		if addr, ok := loc["address"].(string); ok {
			locBuilder.Address(addr)
		}
		builder.Location(locBuilder.Build())
	}

	return builder.Build()
}

// buildAttendeeList constructs attendees from args and user context.
func buildAttendeeList(ctx context.Context, args map[string]any) []*larkcalendar.CalendarEventAttendee {
	var attendees []*larkcalendar.CalendarEventAttendee

	if raw := sliceArg(args, "attendees"); len(raw) > 0 {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				att := buildSingleAttendee(m)
				if att != nil {
					attendees = append(attendees, att)
				}
			}
		}
	}

	// Add user_open_id (from args or context) as a user attendee if not already present.
	userOpenID := stringArg(args, "user_open_id")
	if userOpenID == "" {
		userOpenID = OpenIDFromContext(ctx)
	}
	if userOpenID != "" {
		found := false
		for _, a := range attendees {
			if a.UserId != nil && *a.UserId == userOpenID {
				found = true
				break
			}
		}
		if !found {
			attendees = append(attendees, larkcalendar.NewCalendarEventAttendeeBuilder().
				Type("user").UserId(userOpenID).Build())
		}
	}

	return attendees
}

func buildSingleAttendee(m map[string]any) *larkcalendar.CalendarEventAttendee {
	attType, _ := m["type"].(string)
	id, _ := m["id"].(string)
	if attType == "" || id == "" {
		return nil
	}
	builder := larkcalendar.NewCalendarEventAttendeeBuilder().Type(attType)
	switch attType {
	case "user":
		builder.UserId(id)
	case "chat":
		builder.ChatId(id)
	case "resource":
		builder.RoomId(id)
	case "third_party":
		builder.ThirdPartyEmail(id)
	}
	return builder.Build()
}
