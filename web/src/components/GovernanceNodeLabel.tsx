import {Typography} from "antd";
import {governanceNodeInfo,type GovernanceNodeContext} from "../api/governanceNodeInfo";
import "./GovernanceNodeLabel.css";

export function GovernanceNodeLabel({id,context}:{id:string;context:GovernanceNodeContext}){
 const info=governanceNodeInfo(id,context);
 return <div className="governance-node-description"><strong>{info.name}</strong><Typography.Paragraph className="helper-text" ellipsis={{rows:2,expandable:true,symbol:"展开选择信息"}}>{info.description}</Typography.Paragraph>{info.reference&&<span className="helper-text">{info.reference}</span>}</div>;
}
