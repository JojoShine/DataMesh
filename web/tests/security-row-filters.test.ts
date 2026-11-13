import {expect,it} from "vitest";
import {parseSecurityRowFilters} from "../src/api/security";
it("保留过滤值的数据类型并支持空值比较",()=>{
 expect(parseSecurityRowFilters('[{"field":"age","operator":"gte","value":18},{"field":"department","operator":"in","value":["教育","公安"]},{"field":"phone","operator":"is_null"}]')).toEqual([{field:"age",operator:"gte",value:18},{field:"department",operator:"in",value:["教育","公安"]},{field:"phone",operator:"is_null"}]);
});
it.each(['{}','[{"field":"id","operator":"execute","value":"drop"}]','[{"field":"id","operator":"in","value":[]}]','[{"field":"id","operator":"eq","value":{}}]','[{"field":"id","operator":"eq","value":null}]'])("无效条件不能作为可保存的策略：%s",text=>{expect(()=>parseSecurityRowFilters(text)).toThrow()});

it.each(['9007199254740993','0.10000000000000000001','1e400'])("不静默舍入策略中的数值：%s",literal=>{expect(()=>parseSecurityRowFilters(`[{"field":"id","operator":"eq","value":${literal}}]`)).toThrow(/精度/)});
it("高精度值可使用精确字符串，普通等价写法可使用数字",()=>{
 expect(parseSecurityRowFilters('[{"field":"id","operator":"eq","value":"9007199254740993"}]')[0].value).toBe("9007199254740993");
 expect(parseSecurityRowFilters('[{"field":"amount","operator":"eq","value":1.00e2}]')[0].value).toBe(100);
});
