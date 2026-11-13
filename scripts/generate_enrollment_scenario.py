"""Export the enrollment portion of the current business initialization version."""
import argparse
import csv
import json
from pathlib import Path
from business_scenarios import build_tables, table_schema_sql, insertion_sql

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--output-dir',type=Path,required=True);parser.add_argument('--tables',nargs='+');args=parser.parse_args()
 tables=[t for t in build_tables() if t.scenario=='enrollment']
 if args.tables:
  missing=set(args.tables)-{t.name for t in tables}
  if missing:parser.error('Unknown tables: '+', '.join(sorted(missing)))
  tables=[t for t in tables if t.name in args.tables]
 args.output_dir.mkdir(parents=True,exist_ok=True);sql=['BEGIN;','CREATE SCHEMA IF NOT EXISTS enrollment;','GRANT USAGE ON SCHEMA enrollment TO datamesh_cdc;'];manifest=[]
 for table in tables:
  with (args.output_dir/(table.name+'.csv')).open('w',newline='',encoding='utf-8-sig') as stream:
   writer=csv.writer(stream);writer.writerow([c['name'] for c in table.columns]);writer.writerows(table.rows)
  sql.extend([table_schema_sql(table),insertion_sql(table),'GRANT SELECT ON '+table.qualified+' TO datamesh_cdc;'])
  manifest.append({'table':table.name,'name':table.title,'department':table.provider,'fields':table.columns,'rows':len(table.rows)})
 sql.append('COMMIT;');(args.output_dir/'source.sql').write_text('\n'.join(sql)+'\n');(args.output_dir/'datasets.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
 (args.output_dir/'verify-join.sql').write_text('SELECT count(*),count(DISTINCT p.child_id) FROM enrollment.education_students e JOIN enrollment.police_children p USING(child_id) JOIN enrollment.civil_marriages c USING(guardian_id) JOIN enrollment.property_ownership h USING(guardian_id) JOIN enrollment.social_insurance s USING(guardian_id) JOIN enrollment.market_entities m USING(guardian_id) JOIN enrollment.tax_registrations t ON t.entity_code=m.entity_code;\n')
 print(json.dumps({'tables':len(tables),'rows':[len(t.rows) for t in tables]}))

if __name__=='__main__':main()
