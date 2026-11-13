import {Alert,Spin} from "antd";
import {securityResourceFromPath} from "./SecurityOverview";
import {SecurityControls} from "./SecurityControls";
import {usePath} from "../app/navigation";
import {useQuery} from "../hooks/useQuery";
import {query} from "../api/client";
import type {Field} from "../api/types";
import {Failure} from "../components/common";
export function SecurityResourceLabels(){const resource=securityResourceFromPath(usePath());const detail=useQuery<{fields:Field[]}>(resource?"/api/v1/assets/resource-detail?"+query({...resource}):null);if(!resource)return <Alert type="warning" title="请从资产详情选择需要复核的资源版本"/>;if(detail.error)return <Failure error={detail.error}/>;if(!detail.data)return <Spin/>;return <SecurityControls kind="classification" resource={resource} resourceFields={detail.data.fields}/>}
