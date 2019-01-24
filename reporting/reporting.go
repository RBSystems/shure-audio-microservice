package reporting

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/byuoitav/common/db"
	"github.com/byuoitav/common/log"
	"github.com/byuoitav/common/nerr"
	"github.com/byuoitav/common/v2/events"
	eventhelper "github.com/byuoitav/shure-audio-microservice/event"
	"github.com/byuoitav/shure-audio-microservice/publishing"
	"github.com/byuoitav/shure-audio-microservice/state"
	"github.com/fatih/color"
)

//PORT .
const PORT = 2202

//Monitor .
func Monitor(building, room string) {

	log.L.Infof("Starting mic reporting in building %s, room %s", building, room)

	//get Shure device
	log.L.Infof("Accessing shure device...")
	shure, err := db.GetDB().GetDevicesByRoomAndRole(fmt.Sprintf("%v-%v", building, room), "Receiver")
	for err != nil {
		log.L.Debugf("%s", color.HiRedString("[publisher] receiver not found: %s, retrying in 3s...", err.Error()))
		time.Sleep(5 * time.Second)
		shure, err = db.GetDB().GetDevicesByRoomAndRole(fmt.Sprintf("%v-%v", building, room), "Receiver")
	}

	if len(shure) == 0 {
		log.L.Debugf("%s", color.HiRedString("[publisher] no reciever detected in room. Aborting publisher..."))
		return
	}

	if len(shure) > 1 {
		msg := fmt.Sprintf("[error] detected %v recievers, expecting 1.", len(shure))
		log.L.Debugf("%s", color.HiRedString("[publisher] %s", msg))
		publishing.ReportError(msg, os.Getenv("SYSTEM_ID"), building, room)
		return
	}

	log.L.Infof("%s", color.HiBlueString("[reporting] connecting to device %s at address %s...", shure[0].Name, shure[0].Address))

	connection, err := net.DialTimeout("tcp", shure[0].Address+":2202", time.Second*3)
	if err != nil {
		errorMessage := fmt.Sprintf("[error] Could not connect to device: %s", err.Error())
		color.Set(color.FgHiYellow, color.Bold)
		log.L.Debugf(errorMessage)
		color.Unset()
		publishing.ReportError(errorMessage, shure[0].Name, building, room)
		return
	}

	reader := bufio.NewReader(connection)
	log.L.Infof("%s", color.HiGreenString("[reporting] successfully connected to device %s", shure[0].Name))
	log.L.Infof("%s", color.HiBlueString("[reporting] listening for events..."))

	for {

		data, err := reader.ReadString('>')
		if err != nil {
			msg := fmt.Sprintf("problem reading receiver string: %s", err.Error())
			publishing.ReportError(msg, os.Getenv("SYSTEM_ID"), building, room)
			continue
		}
		log.L.Debugf("%s", color.HiGreenString("[reporting] read string: %s", data))

		eventInfos, err := GetEventInfo(data, fmt.Sprintf("%v-%v", building, room))
		if err != nil {
			msg := fmt.Sprintf("problem reading receiver string: %s", err.Error())
			publishing.ReportError(msg, os.Getenv("SYSTEM_ID"), building, room)
		}

		for _, event := range eventInfos {
			if event.TargetDevice.DeviceID == "" {
				continue
			}

			err = publishing.PublishEvent(false, event, building, room)
			if err != nil {
				msg := fmt.Sprintf("failed to publish event: %s", err.Error())
				publishing.ReportError(msg, os.Getenv("SYSTEM_ID"), building, room)
			}

		}

	}

}

//GetEventInfo .
func GetEventInfo(data, roomID string) ([]events.Event, error) {

	//identify device name
	re := regexp.MustCompile("REP [\\d]")
	channel := re.FindString(data)

	if len(channel) == 0 {
		msg := "no data"
		log.L.Debugf("%s", color.HiYellowString("[reporting] %s", msg))
		return []events.Event{}, nil
	}

	deviceName := fmt.Sprintf("%v-MIC%s", roomID, channel[len(channel)-1:])

	log.L.Debugf("[reporting] device %s reporting", deviceName)
	data = re.ReplaceAllString(data, "")

	eventArray := []events.Event{}

	event := events.Event{
		TargetDevice: events.GenerateBasicDeviceInfo(deviceName),
	}

	//identify event type: interference, power, battery
	E, er := GetEventType(data)
	if er != nil {
		return []events.Event{}, nil
	}

	err := E.FillEventInfo(data, &event)
	if strings.EqualFold(event.Value, eventhelper.FLAG) || len(event.Key) == 0 {
		log.L.Debugf("Ignoring event: %v", data)
		return []events.Event{}, nil
	} else if err != nil {
		log.L.Debugf("There was an error: %v", err.Error())
		eventArray = append(eventArray, event)
		return eventArray, err
	}

	if strings.Contains(event.Key, "minutes") {
		log.L.Debugf("Translating to minutes hours from: %v", event)
		//translate to hours/minutes, generate new event

		val, err := strconv.Atoi(event.Value)
		if err == nil {
			hours := val / 60
			minutes := val % 60

			hoursMinuteEvent := events.Event{
				Key:          "battery level (hours:minute remaining",
				Value:        fmt.Sprintf("%v:%v", hours, minutes),
				TargetDevice: events.GenerateBasicDeviceInfo(deviceName),
			}
			hoursMinuteEvent.AddToTags(events.DetailState, events.AutoGenerated)
			eventArray = append(eventArray, hoursMinuteEvent)
			log.L.Debugf("Generated Event: %+v", hoursMinuteEvent)
		}
	}

	eventArray = append(eventArray, event)
	return eventArray, nil
}

//GetEventType .
func GetEventType(data string) (eventhelper.Context, *nerr.E) {

	if strings.Contains(data, state.Interference.String()) {
		return eventhelper.Context{E: eventhelper.Interference{}}, nil
	} else if strings.Contains(data, state.Power.String()) {
		return eventhelper.Context{E: eventhelper.Power{}}, nil
	} else if strings.Contains(data, state.Battery.String()) {
		return eventhelper.Context{E: eventhelper.Battery{}}, nil
	}
	return eventhelper.Context{}, nerr.Create("Couldn't generate event type", "invalid")
}
