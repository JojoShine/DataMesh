import {DatePicker,TimePicker} from "antd";
import dayjs from "dayjs";
import utc from "dayjs/plugin/utc";
import "dayjs/locale/zh-cn";
import "./GovernanceDateInputs.css";

dayjs.extend(utc);
type StringDateProps={id?:string;value?:string;onChange?:(value:string)=>void;placeholder?:string};
// These controls edit wall-clock text. The caller applies the selected or local
// timezone when submitting; the browser must not shift the displayed time.
export function GovernanceDateTimePicker({id,value,onChange,placeholder}:StringDateProps){
 const now=dayjs.utc(dayjs().format("YYYY-MM-DDTHH:mm"));
 return <DatePicker classNames={{popup:{root:"governance-date-popup"}}} id={id} value={value?dayjs.utc(value):null} defaultPickerValue={now} showNow={false} showTime={{format:"HH:mm"}} format={["YYYY-MM-DD HH:mm","YYYY-MM-DDTHH:mm"]} onChange={date=>onChange?.(date?.format("YYYY-MM-DDTHH:mm")??"")} placeholder={placeholder??"请选择日期和时间"}/>;
}
export function GovernanceTimePicker({id,value,onChange,placeholder}:StringDateProps){
 return <TimePicker classNames={{popup:{root:"governance-date-popup"}}} id={id} value={value?dayjs.utc(`2000-01-01T${value}`):null} defaultPickerValue={dayjs.utc("2000-01-01T00:00")} format="HH:mm" showNow={false} needConfirm={false} onChange={date=>onChange?.(date?.format("HH:mm")??"")} placeholder={placeholder??"请选择时间"}/>;
}
